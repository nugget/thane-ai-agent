package tools

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// jsonFieldNames returns the wire names of a struct's exported fields,
// skipping those the encoder omits.
func jsonFieldNames(t reflect.Type) []string {
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TestLoopSpecSchemaCoversEveryField is the gate the loop spec did not
// have. loopSpecSchema is hand-authored and Spec is not, so a field can
// reach the struct, decode correctly, and never be mentioned to the
// model — which is invisible rather than broken, and therefore does not
// fail anything.
//
// This is the same failure shape as a native tool that is registered but
// absent from the tool catalog, which TestNativeToolLiteralsAreCatalogued
// already guards, and the same shape as thane_loop_create silently
// dropping a tiers array it had no property for.
//
// The direction that matters is struct → schema. The reverse is checked
// separately and more loosely, because the schema legitimately documents
// keys the decoder accepts for backward compatibility.
func TestLoopSpecSchemaCoversEveryField(t *testing.T) {
	props := schemaProperties(t, loopSpecSchema("parity"))

	for _, field := range jsonFieldNames(reflect.TypeOf(looppkg.Spec{})) {
		if reason, deliberate := specFieldsNotOffered[field]; deliberate {
			if reason == "" {
				t.Errorf("Spec field %q is excluded with no reason recorded", field)
			}
			continue
		}
		if _, ok := props[field]; !ok {
			t.Errorf("Spec field %q has no property in loopSpecSchema — it decodes but the model is never told it exists. Add a property, or record it in specFieldsNotOffered with the reason it is not the model's to set", field)
		}
	}
}

// specFieldsNotOffered records the Spec fields deliberately absent from
// the model-facing schema, and why. The point of the map is that adding
// a field to Spec now forces the choice to be made and written down,
// rather than the field being invisible by default.
var specFieldsNotOffered = map[string]string{
	"parent_id": "runtime-only: set at launch, and live loop IDs change per launch, so the durable parent reference is parent_name",
	"origin":    "provenance the system stamps, not authorship the model claims",

	"delegation_gating": "the spec-level door is profile.delegation_gating, which the schema does offer; this top-level field is the runtime variant",
	"routing_factors":   "request-routing internals; the authoring surface for routing is profile",

	// Narrow but arguably authorable: an interactive loop can set it to
	// guarantee a reply. Excluded because it only applies to
	// request_reply runs, which neither guided door creates — thane_now
	// owns that shape. Raised on #1287 rather than widened unilaterally.
	"fallback_content": "applies only to request_reply runs, which the loop-authoring doors do not create",
}

// TestLoopOutputSpecSchemaCoversEveryField guards the nested declaration
// where the gap actually bit: output.tiers reached OutputSpec before the
// guided tool had anywhere to put it.
func TestLoopOutputSpecSchemaCoversEveryField(t *testing.T) {
	props := schemaProperties(t, loopSpecSchema("parity"))
	outputs, ok := props["outputs"].(map[string]any)
	if !ok {
		t.Fatal("loopSpecSchema has no outputs property")
	}
	items, ok := outputs["items"].(map[string]any)
	if !ok {
		t.Fatal("outputs has no items schema")
	}
	itemProps := schemaProperties(t, items)

	for _, field := range jsonFieldNames(reflect.TypeOf(looppkg.OutputSpec{})) {
		if _, ok := itemProps[field]; !ok {
			t.Errorf("OutputSpec field %q has no property in the outputs item schema — a loop can declare it and no model will know to", field)
		}
	}
}

// TestSpecFieldsNotOfferedStayReal keeps the allowlist from outliving
// the fields it excuses. An entry for a field that no longer exists is
// worse than none: it reads as a considered decision about something
// that is not there, and it would silently excuse a future field that
// happened to take the same name.
func TestSpecFieldsNotOfferedStayReal(t *testing.T) {
	actual := make(map[string]bool)
	for _, f := range jsonFieldNames(reflect.TypeOf(looppkg.Spec{})) {
		actual[f] = true
	}
	for field := range specFieldsNotOffered {
		if !actual[field] {
			t.Errorf("specFieldsNotOffered lists %q, which is no longer a Spec field", field)
		}
	}
}
