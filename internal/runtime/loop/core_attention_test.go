package loop

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

func TestWakeCoreLoopDeliversSharedCoreAttentionEnvelope(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	target, err := New(Config{
		Name: "core-attention",
		Task: "Review core attention requests.",
		Metadata: map[string]string{
			"core_attention_target": "true",
		},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New target: %v", err)
	}
	if err := registry.Register(target); err != nil {
		t.Fatalf("Register target: %v", err)
	}

	bus := messages.NewBus(nil)
	var got messages.Envelope
	bus.RegisterRoute(messages.DestinationLoop, func(_ context.Context, env messages.Envelope) (messages.DeliveryResult, error) {
		got = env
		return messages.DeliveryResult{Route: "test", Status: messages.DeliveryDelivered}, nil
	})

	result, err := WakeCoreLoop(context.Background(), registry, bus, CoreWakeRequest{
		From:            messages.Identity{Kind: messages.IdentitySystem, Name: "test_subsystem"},
		Concern:         "A subsystem needs core review.",
		SuggestedAction: "Decide whether to escalate.",
		Priority:        messages.PriorityUrgent,
		Scope:           []string{CoreAttentionScope, "test_scope"},
		ForceSupervisor: true,
	})
	if err != nil {
		t.Fatalf("WakeCoreLoop: %v", err)
	}

	if result.Target.LoopID != target.ID() || result.Target.LoopName != target.Name() {
		t.Fatalf("target = %#v, want %s/%s", result.Target, target.ID(), target.Name())
	}
	if got.To.Target != target.ID() || got.To.Selector != messages.SelectorID {
		t.Fatalf("to = %#v, want target id %q", got.To, target.ID())
	}
	if got.Priority != messages.PriorityUrgent {
		t.Fatalf("priority = %q, want urgent", got.Priority)
	}
	if len(got.Scope) != 2 || got.Scope[0] != CoreAttentionScope || got.Scope[1] != "test_scope" {
		t.Fatalf("scope = %#v, want deduped core + test scope", got.Scope)
	}
	payload, ok := got.Payload.(messages.LoopNotifyPayload)
	if !ok {
		t.Fatalf("payload type = %T, want LoopNotifyPayload", got.Payload)
	}
	if payload.Kind != CoreAttentionRequestKind || !payload.ForceSupervisor {
		t.Fatalf("payload = %#v, want default core attention supervisor wake", payload)
	}
	// This request comes from system code, which has no later iteration
	// to receive a reply — so it earns no reply tag, and the recipient's
	// tool surface is not widened to serve a return leg that cannot run.
	if len(payload.Tags) != 0 {
		t.Fatalf("payload tags = %#v, want none for a system sender", payload.Tags)
	}
}

// TestCoreWakeEnvelopeReplyTagFollowsAnswerability pins the tag to the
// one case that can use it. The loops tag carries definition and
// lifecycle mutation tools, so it is granted to make the return leg
// callable — never as a side effect of asking for a determination.
func TestCoreWakeEnvelopeReplyTagFollowsAnswerability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		from     messages.Identity
		wantTags []string
	}{
		{
			name:     "live loop can be woken back",
			from:     messages.Identity{Kind: messages.IdentityLoop, ID: "loop-42", Name: "garage-watch"},
			wantTags: []string{coreAttentionReplyTag},
		},
		{
			name: "loop identity without an id is unaddressable",
			from: messages.Identity{Kind: messages.IdentityLoop, Name: "anonymous"},
		},
		{
			name: "system sender has no later iteration",
			from: messages.Identity{Kind: messages.IdentitySystem, Name: "document_root_syncer"},
		},
		{
			name: "delegate is a one-shot",
			from: messages.Identity{Kind: messages.IdentityDelegate, Name: "delegate"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := CoreWakeEnvelope(CoreAttentionTarget{LoopID: "core"}, CoreWakeRequest{
				From:    tt.from,
				Concern: "Something deserves a decision.",
			})
			payload, ok := env.Payload.(messages.LoopNotifyPayload)
			if !ok {
				t.Fatalf("payload type = %T, want LoopNotifyPayload", env.Payload)
			}
			if len(payload.Tags) != len(tt.wantTags) {
				t.Fatalf("payload tags = %#v, want %#v", payload.Tags, tt.wantTags)
			}
			for i, tag := range tt.wantTags {
				if payload.Tags[i] != tag {
					t.Fatalf("payload tags = %#v, want %#v", payload.Tags, tt.wantTags)
				}
			}
		})
	}
}
