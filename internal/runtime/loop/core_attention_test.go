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
}
