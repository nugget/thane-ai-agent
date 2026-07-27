package tools

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// jsonFieldNames returns the wire names encoding/json would use for a
// struct's exported fields.
//
// Only `json:"-"` is omitted. An exported field with no json tag, or one
// carrying only options like `json:",omitempty"`, is still encoded —
// under its Go name — so treating those as absent would let exactly the
// kind of field this gate exists to catch slip through it.
func jsonFieldNames(t reflect.Type) []string {
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = field.Name
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TestJSONFieldNamesMatchesEncoder pins the helper against the encoder
// it stands in for. The gate is only as good as its idea of which fields
// are on the wire: a field carrying no json tag is still encoded, under
// its Go name, and reading that as "absent" would let the gate skip
// precisely the field it exists to catch.
func TestJSONFieldNamesMatchesEncoder(t *testing.T) {
	type sample struct {
		Tagged   string `json:"tagged"`
		OnlyOpts string `json:",omitempty"`
		NoTag    string
		Excluded string `json:"-"`
		//nolint:unused // presence is the point: unexported fields are not encoded
		unexported string
	}

	got := jsonFieldNames(reflect.TypeOf(sample{}))
	want := []string{"NoTag", "OnlyOpts", "tagged"}
	if len(got) != len(want) {
		t.Fatalf("jsonFieldNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("jsonFieldNames = %v, want %v", got, want)
		}
	}
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

// TestSchemasRequireOnlyDefinedProperties catches the half-removal that
// prompted it: `mode` was deleted from thane_loop_create's output object
// and left in its `required` list, so a schema-aware caller had to send
// a field the schema did not define or fail validation — no answer was
// correct. A required key with no property is internally inconsistent
// and mechanically checkable, which makes it the reviewer's job only
// until it is a test's.
func TestSchemasRequireOnlyDefinedProperties(t *testing.T) {
	for name, schema := range map[string]map[string]any{
		"loopSpecSchema":        loopSpecSchema("parity"),
		"thaneLoopCreateSchema": thaneLoopCreateSchema(),
	} {
		t.Run(name, func(t *testing.T) {
			checkRequiredAreDefined(t, name, schema)
		})
	}
}

// checkRequiredAreDefined walks nested objects and array items, because
// the offending list was two levels down inside output.
func checkRequiredAreDefined(t *testing.T, path string, node map[string]any) {
	t.Helper()

	props, _ := node["properties"].(map[string]any)
	if required, ok := node["required"].([]string); ok {
		for _, key := range required {
			if _, defined := props[key]; !defined {
				t.Errorf("%s requires %q but defines no such property", path, key)
			}
		}
	}
	for key, child := range props {
		if obj, ok := child.(map[string]any); ok {
			checkRequiredAreDefined(t, path+"."+key, obj)
			if items, ok := obj["items"].(map[string]any); ok {
				checkRequiredAreDefined(t, path+"."+key+"[]", items)
			}
		}
	}
}
