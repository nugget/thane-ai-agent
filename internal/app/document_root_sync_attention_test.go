package app

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func TestDocRootSyncAttentionNotifierSendsCoreWake(t *testing.T) {
	bus := messages.NewBus(testLogger())
	var got messages.Envelope
	bus.RegisterRoute(messages.DestinationLoop, func(_ context.Context, env messages.Envelope) (messages.DeliveryResult, error) {
		got = env
		return messages.DeliveryResult{Route: "test", Status: messages.DeliveryDelivered}, nil
	})

	loops := looppkg.NewRegistry()
	target, err := looppkg.New(looppkg.Config{
		Name: "core-attention",
		Task: "Review core attention requests.",
		Metadata: map[string]string{
			"core_attention_target": "true",
		},
	}, looppkg.Deps{Runner: testLoopRunner{}})
	if err != nil {
		t.Fatalf("New target loop: %v", err)
	}
	if err := loops.Register(target); err != nil {
		t.Fatalf("Register target loop: %v", err)
	}

	a := &App{messageBus: bus, loopRegistry: loops}
	transition := syncStateTransition{
		Kind: syncTransitionAttentionRequired,
		Current: syncState{
			Root:       "kb",
			OK:         true,
			Outcome:    provenance.SyncBlocked,
			Ahead:      0,
			Behind:     2,
			LocalHead:  "local",
			RemoteHead: "remote",
			Detail:     "first untrusted commit abc123",
		},
	}
	if err := a.notifyDocRootSyncTransition(context.Background(), transition); err != nil {
		t.Fatalf("notifyDocRootSyncTransition: %v", err)
	}

	if got.From.Kind != messages.IdentitySystem || got.From.Name != "document_root_syncer" {
		t.Fatalf("from = %#v, want document_root_syncer system identity", got.From)
	}
	if got.To.Target != target.ID() || got.To.Selector != messages.SelectorID {
		t.Fatalf("to = %#v, want target loop id %q", got.To, target.ID())
	}
	if got.Priority != messages.PriorityUrgent {
		t.Fatalf("priority = %q, want urgent", got.Priority)
	}
	payload, ok := got.Payload.(messages.LoopNotifyPayload)
	if !ok {
		t.Fatalf("payload type = %T, want LoopNotifyPayload", got.Payload)
	}
	if payload.Kind != docRootSyncTransitionKind || !payload.ForceSupervisor {
		t.Fatalf("payload = %#v, want doc-root sync supervisor transition", payload)
	}
	if !strings.Contains(payload.Concern, "first untrusted commit abc123") {
		t.Fatalf("concern = %q, want refusal detail in primary concern", payload.Concern)
	}
	if strings.Contains(strings.ToLower(payload.SuggestedAction), "send") {
		t.Fatalf("suggested action should not command direct human delivery: %q", payload.SuggestedAction)
	}
	if len(payload.Events) != 1 {
		t.Fatalf("events len = %d, want 1", len(payload.Events))
	}
	event := payload.Events[0]
	if event.Source != "document_root_sync" || event.Type != string(syncTransitionAttentionRequired) {
		t.Fatalf("event = %#v, want document_root_sync attention event", event)
	}
	if event.Metadata["root"] != "kb" || event.Metadata["outcome"] != string(provenance.SyncBlocked) || event.Metadata["detail"] != "first untrusted commit abc123" {
		t.Fatalf("event metadata = %#v", event.Metadata)
	}
	changedDetail := transition
	changedDetail.Current.Detail = "first untrusted commit def456"
	if event.ID == docRootSyncTransitionEvent(changedDetail).ID {
		t.Fatalf("event ID %q should differ when transition detail differs", event.ID)
	}
}

func TestDocRootSyncRecoveryWakeIsNotSupervisorForced(t *testing.T) {
	env := docRootSyncTransitionEnvelope(looppkg.CoreAttentionTarget{LoopID: "loop-1"}, syncStateTransition{
		Kind: syncTransitionRecovered,
		Previous: syncState{
			Root:    "kb",
			OK:      true,
			Outcome: provenance.SyncBlocked,
			Detail:  "first untrusted commit abc123",
		},
		Current: syncState{
			Root:    "kb",
			OK:      true,
			Outcome: provenance.SyncFastForwarded,
		},
		HasPrevious: true,
	})

	if env.Priority != messages.PriorityNormal {
		t.Fatalf("priority = %q, want normal", env.Priority)
	}
	payload, ok := env.Payload.(messages.LoopNotifyPayload)
	if !ok {
		t.Fatalf("payload type = %T, want LoopNotifyPayload", env.Payload)
	}
	if payload.ForceSupervisor {
		t.Fatal("recovery wake should not force supervisor")
	}
	if !strings.Contains(payload.Concern, "is fast_forwarded after blocked") {
		t.Fatalf("concern = %q, want actual recovered outcome", payload.Concern)
	}
	if !strings.Contains(payload.SuggestedAction, "no direct human message") {
		t.Fatalf("suggested action = %q, want no-direct-human guidance", payload.SuggestedAction)
	}
}
