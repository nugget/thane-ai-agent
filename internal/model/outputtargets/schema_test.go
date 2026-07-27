package outputtargets

import (
	"reflect"
	"strings"
	"testing"
)

func TestSchemaShapesEverySlot(t *testing.T) {
	schema := testTarget().Schema()
	if schema["type"] != "object" {
		t.Fatalf("schema type = %v", schema["type"])
	}

	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties is %T", schema["properties"])
	}
	if len(properties) != 4 {
		t.Fatalf("expected 4 properties, got %d", len(properties))
	}

	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("required is %T", schema["required"])
	}
	if !reflect.DeepEqual(required, []string{"value"}) {
		t.Fatalf("required = %v, want [value]", required)
	}

	value := properties["value"].(map[string]any)
	if value["type"] != "string" {
		t.Fatalf("value type = %v", value["type"])
	}
	if value["maxLength"] != 6 {
		t.Fatalf("value maxLength = %v, want 6", value["maxLength"])
	}
	if desc, _ := value["description"].(string); !strings.Contains(desc, "Maximum 6 characters") {
		t.Fatalf("value description does not state the budget: %q", desc)
	}

	fraction := properties["fraction"].(map[string]any)
	if fraction["type"] != "number" || fraction["minimum"] != 0 || fraction["maximum"] != 1 {
		t.Fatalf("fraction schema = %v", fraction)
	}

	tint := properties["tint"].(map[string]any)
	if tint["type"] != "string" {
		t.Fatalf("tint type = %v", tint["type"])
	}
	if pattern, _ := tint["pattern"].(string); pattern == "" {
		t.Fatal("color slot has no pattern")
	}
}

func TestSchemaIsAdvisory(t *testing.T) {
	// Matching loopSpecSchema: no additionalProperties:false, so a
	// provider never silently rejects a key. Normalize is what teaches.
	for _, target := range All() {
		if _, present := target.Schema()["additionalProperties"]; present {
			t.Errorf("target %q sets additionalProperties; unknown keys are rejected by Normalize with a teaching error instead", target.ID)
		}
	}
}

func TestSchemaCoversRegisteredTargets(t *testing.T) {
	for _, target := range All() {
		schema := target.Schema()
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("target %q has no properties", target.ID)
		}
		for _, slot := range target.Slots {
			property, present := properties[slot.Name]
			if !present {
				t.Errorf("target %q slot %q missing from schema", target.ID, slot.Name)
				continue
			}
			fragment := property.(map[string]any)
			if fragment["type"] == nil {
				t.Errorf("target %q slot %q has an untyped schema fragment", target.ID, slot.Name)
			}
		}
	}
}

func TestToolDescriptionNamesOutputAndEntity(t *testing.T) {
	target, ok := Lookup("apple_watch.rectangular")
	if !ok {
		t.Fatal("apple_watch.rectangular is not registered")
	}
	description := target.ToolDescription("watch_status", "sensor.thane_watch_status")
	for _, want := range []string{"watch_status", "sensor.thane_watch_status", "replaces the whole payload"} {
		if !strings.Contains(description, want) {
			t.Errorf("description missing %q: %s", want, description)
		}
	}
}
