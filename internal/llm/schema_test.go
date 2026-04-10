package llm

import "testing"

func TestStripTopLevelCompositionKeywords_PreservesNestedComposition(t *testing.T) {
	t.Parallel()

	in := map[string]any{
		"type": "object",
		"anyOf": []any{
			map[string]any{"required": []any{"loop_id"}},
			map[string]any{"required": []any{"name"}},
		},
		"properties": map[string]any{
			"duration": map[string]any{
				"anyOf": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "number"},
				},
			},
		},
	}

	got, removed := StripTopLevelCompositionKeywords(in)
	if len(removed) != 1 || removed[0] != "anyOf" {
		t.Fatalf("removed = %#v, want [\"anyOf\"]", removed)
	}
	if _, ok := got["anyOf"]; ok {
		t.Fatalf("top-level anyOf still present: %#v", got)
	}
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", got["properties"])
	}
	duration, ok := props["duration"].(map[string]any)
	if !ok {
		t.Fatalf("duration type = %T, want map[string]any", props["duration"])
	}
	if _, ok := duration["anyOf"]; !ok {
		t.Fatalf("nested anyOf missing after sanitize: %#v", duration)
	}
}

func TestStripTopLevelCompositionKeywords_MergesVariantProperties(t *testing.T) {
	t.Parallel()

	in := map[string]any{
		"oneOf": []any{
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"entity_id": map[string]any{"type": "string"},
				},
			},
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"area_id": map[string]any{"type": "string"},
				},
			},
		},
	}

	got, removed := StripTopLevelCompositionKeywords(in)
	if len(removed) != 1 || removed[0] != "oneOf" {
		t.Fatalf("removed = %#v, want [\"oneOf\"]", removed)
	}
	if got["type"] != "object" {
		t.Fatalf("type = %#v, want object", got["type"])
	}
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", got["properties"])
	}
	if _, ok := props["entity_id"]; !ok {
		t.Fatalf("entity_id missing from merged properties: %#v", props)
	}
	if _, ok := props["area_id"]; !ok {
		t.Fatalf("area_id missing from merged properties: %#v", props)
	}
}
