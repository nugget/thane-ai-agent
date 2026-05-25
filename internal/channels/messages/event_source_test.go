package messages

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestLoopWakeTargetYAMLRoundTripSnakeCase pins the Codex P2 fix: the
// struct's YAML tags match its JSON tags (snake_case) so operator-edited
// config files can use loop_id / force_supervisor / etc. Pre-fix,
// only JSON tags were declared and YAML defaulted to lowercase
// concatenation (loopid / forcesupervisor), silently dropping
// operator-supplied values that the docs and examples described.
func TestLoopWakeTargetYAMLRoundTripSnakeCase(t *testing.T) {
	t.Parallel()

	source := `loop_id: rt-abc
name: my_handler
force_supervisor: true
priority: urgent
instructions: "act fast"
tags:
  - owner
  - urgent_pickup
`
	var got LoopWakeTarget
	if err := yaml.Unmarshal([]byte(source), &got); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if got.LoopID != "rt-abc" {
		t.Errorf("LoopID = %q, want rt-abc (snake_case loop_id not honored)", got.LoopID)
	}
	if got.Name != "my_handler" {
		t.Errorf("Name = %q, want my_handler", got.Name)
	}
	if !got.ForceSupervisor {
		t.Errorf("ForceSupervisor = false, want true (snake_case force_supervisor not honored)")
	}
	if got.Priority != PriorityUrgent {
		t.Errorf("Priority = %q, want urgent", got.Priority)
	}
	if got.Instructions != "act fast" {
		t.Errorf("Instructions = %q, want %q", got.Instructions, "act fast")
	}
	if len(got.Tags) != 2 || got.Tags[0] != "owner" || got.Tags[1] != "urgent_pickup" {
		t.Errorf("Tags = %v, want [owner urgent_pickup]", got.Tags)
	}

	// Re-emit and confirm snake_case names survive a round-trip.
	out, err := yaml.Marshal(got)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	for _, key := range []string{"loop_id", "force_supervisor"} {
		if !strings.Contains(string(out), key+":") {
			t.Errorf("re-emitted YAML missing %q field: %s", key, out)
		}
	}
}

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
