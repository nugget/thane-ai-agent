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

func TestNewEventSourceEnvelopeCarriesWakeTags(t *testing.T) {
	t.Parallel()

	env, err := NewEventSourceEnvelope(
		Identity{Kind: IdentitySystem, Name: "contacts"},
		LoopWakeTarget{Name: "email-triage", Tags: []string{"owner", "high_trust"}},
		"contacts",
		[]LoopEventPayload{{Source: "contacts", Type: "match", ID: "1"}},
	)
	if err != nil {
		t.Fatalf("NewEventSourceEnvelope: %v", err)
	}
	payload, ok := env.Payload.(LoopNotifyPayload)
	if !ok {
		t.Fatalf("payload type = %T, want LoopNotifyPayload", env.Payload)
	}
	if len(payload.Tags) != 2 || payload.Tags[0] != "owner" || payload.Tags[1] != "high_trust" {
		t.Fatalf("payload.Tags = %v, want [owner high_trust]", payload.Tags)
	}
}

func TestParseLoopWakeTargetExtractsTags(t *testing.T) {
	t.Parallel()

	t.Run("string slice", func(t *testing.T) {
		raw := map[string]any{
			"name": "triage",
			"tags": []string{"owner", "", " untrusted "},
		}
		target, ok, err := ParseLoopWakeTarget(raw)
		if err != nil {
			t.Fatalf("ParseLoopWakeTarget: %v", err)
		}
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if len(target.Tags) != 2 || target.Tags[0] != "owner" || target.Tags[1] != "untrusted" {
			t.Fatalf("Tags = %v, want [owner untrusted]", target.Tags)
		}
	})

	t.Run("any slice (post-JSON decode)", func(t *testing.T) {
		raw := map[string]any{
			"name": "triage",
			"tags": []any{"owner", 42, "device_control"},
		}
		target, ok, err := ParseLoopWakeTarget(raw)
		if err != nil {
			t.Fatalf("ParseLoopWakeTarget: %v", err)
		}
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if len(target.Tags) != 2 || target.Tags[0] != "owner" || target.Tags[1] != "device_control" {
			t.Fatalf("Tags = %v, want [owner device_control]", target.Tags)
		}
	})

	t.Run("absent tags", func(t *testing.T) {
		raw := map[string]any{"name": "triage"}
		target, _, err := ParseLoopWakeTarget(raw)
		if err != nil {
			t.Fatalf("ParseLoopWakeTarget: %v", err)
		}
		if target.Tags != nil {
			t.Fatalf("Tags = %v, want nil", target.Tags)
		}
	})
}
