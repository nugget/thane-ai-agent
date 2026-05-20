package messages

import (
	"strings"
	"testing"
)

func TestNewEventSourceEnvelopeRejectsOversizedBatch(t *testing.T) {
	t.Parallel()

	events := make([]LoopEventPayload, MaxLoopEventsPerWake+1)
	for i := range events {
		events[i] = LoopEventPayload{Source: "test", Type: "item", ID: "event"}
	}

	_, err := NewEventSourceEnvelope(
		Identity{Kind: IdentitySystem, Name: "tester"},
		LoopWakeTarget{Name: "curator"},
		"test",
		events,
	)
	if err == nil {
		t.Fatal("expected oversized event batch to be rejected")
	}
	if !strings.Contains(err.Error(), "max per wake") {
		t.Fatalf("error = %q, want max per wake context", err)
	}
}

func TestNewEventSourceEnvelopeAllowsMaxBatch(t *testing.T) {
	t.Parallel()

	events := make([]LoopEventPayload, MaxLoopEventsPerWake)
	for i := range events {
		events[i] = LoopEventPayload{Source: "test", Type: "item", ID: "event"}
	}

	env, err := NewEventSourceEnvelope(
		Identity{Kind: IdentitySystem, Name: "tester"},
		LoopWakeTarget{Name: "curator"},
		"test",
		events,
	)
	if err != nil {
		t.Fatalf("NewEventSourceEnvelope: %v", err)
	}
	payload, ok := env.Payload.(LoopNotifyPayload)
	if !ok {
		t.Fatalf("payload type = %T, want LoopNotifyPayload", env.Payload)
	}
	if len(payload.Events) != MaxLoopEventsPerWake {
		t.Fatalf("events len = %d, want %d", len(payload.Events), MaxLoopEventsPerWake)
	}
}
