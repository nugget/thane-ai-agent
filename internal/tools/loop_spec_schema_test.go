package tools

import (
	"slices"
	"testing"
)

// TestLoopSpecSchemaEnumeratesAuthorableFields guards the #1086 Phase-0
// discoverability win: the loop-definition spec param must advertise its
// authorable fields, not collapse back to a bare {"type":"object"}.
func TestLoopSpecSchemaEnumeratesAuthorableFields(t *testing.T) {
	s := loopSpecSchema("frame this spec")

	if s["type"] != "object" {
		t.Fatalf("type = %v, want object", s["type"])
	}
	if s["description"] != "frame this spec" {
		t.Errorf("description not threaded through: %v", s["description"])
	}
	// Advisory invariant: the schema documents the canonical surface but must
	// stay OPEN, so the decoder's extra/legacy keys (e.g. top-level
	// quality_floor) still validate at the tool-call layer.
	if _, ok := s["additionalProperties"]; ok {
		t.Error("loopSpecSchema must stay open (no additionalProperties) — it is advisory, not restrictive")
	}

	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties missing or wrong type")
	}
	for _, key := range []string{
		"name", "enabled", "task", "operation", "profile", "supervisor",
		"supervisor_prob", "supervisor_profile", "outputs", "tags",
		"exclude_tools", "sleep_min", "sleep_max", "sleep_default", "jitter",
		"max_duration", "max_iter", "on_retrigger", "conditions", "completion",
		"subscriptions", "metadata",
	} {
		if _, ok := props[key].(map[string]any); !ok {
			t.Errorf("spec schema missing enumerated property %q", key)
		}
	}

	// The constrained string fields must be typed string AND carry their
	// enums, so the model picks valid values rather than guessing — and so a
	// schema-following caller never sends the wrong JSON type. completion in
	// particular is a string enum (loop.Completion), not an object.
	for _, key := range []string{"operation", "on_retrigger", "completion"} {
		field, _ := props[key].(map[string]any)
		if field["type"] != "string" {
			t.Errorf("%q must be type string, got %v", key, field["type"])
		}
		enum, ok := field["enum"].([]string)
		if !ok || len(enum) == 0 {
			t.Errorf("%q must carry a non-empty enum", key)
		}
	}
	// on_retrigger must advertise every mode ParseRetriggerMode accepts,
	// including spawn (RetriggerSpawn) — omitting it hides a valid value.
	retrigger, _ := props["on_retrigger"].(map[string]any)
	if enum, _ := retrigger["enum"].([]string); !slices.Contains(enum, "spawn") {
		t.Errorf("on_retrigger enum must include \"spawn\", got %v", retrigger["enum"])
	}

	// profile (and supervisor_profile, same builder) must expose the routing
	// knobs that were the whole point of exposing the envelope.
	prof, ok := props["profile"].(map[string]any)
	if !ok {
		t.Fatal("profile schema missing")
	}
	pprops, _ := prof["properties"].(map[string]any)
	for _, key := range []string{"quality_floor", "instructions", "model", "delegation_gating"} {
		if _, ok := pprops[key]; !ok {
			t.Errorf("profile schema missing %q", key)
		}
	}

	// outputs items must describe ref + the type enum.
	outs, _ := props["outputs"].(map[string]any)
	item, _ := outs["items"].(map[string]any)
	iprops, _ := item["properties"].(map[string]any)
	if _, ok := iprops["ref"]; !ok {
		t.Error("output item schema missing ref")
	}
	if ot, _ := iprops["type"].(map[string]any); ot["enum"] == nil {
		t.Error("output item type must carry an enum")
	}

	// subscriptions[].forecast is a Home Assistant forecast TYPE
	// (daily/hourly/twice_daily/none), not a free-form horizon — it must
	// carry the enum so callers send a valid value.
	subs, _ := props["subscriptions"].(map[string]any)
	subItem, _ := subs["items"].(map[string]any)
	subProps, _ := subItem["properties"].(map[string]any)
	if fc, _ := subProps["forecast"].(map[string]any); fc["enum"] == nil {
		t.Error("subscriptions[].forecast must carry an enum (daily/hourly/twice_daily/none)")
	}
}
