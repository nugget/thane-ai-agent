package loop

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// jsonWireNames returns the names encoding/json would use for a
// struct's exported fields. Only `json:"-"` is skipped: an exported
// field with no tag is still encoded, under its Go name, so treating
// it as absent would let the gate miss what it exists to catch.
func jsonWireNames(t reflect.Type) []string {
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

// TestSpecJSONCarriesEverySpecField guards the seam where a spec field
// can be added everywhere that is visible and still not survive being
// written down.
//
// Spec does not marshal itself: it converts to the specJSON wire type,
// which is a hand-maintained parallel field list. Definitions persist
// as JSON and the agent authors them as JSON, so a field missing from
// that wire type is dropped in both directions — silently, because
// nothing errors and every other surface still shows the field. It
// decodes, it validates, it renders in the schema, and it is simply
// gone after a restart.
//
// That shape is worst for a field that restricts something. The
// forge_account binding was added to Spec, Config, the cascade, the
// docs, the talent, and the model-facing schema, and none of those
// touched specJSON — so a loop bound to a read-only account would have
// come back from a restart bound to nothing, reaching every configured
// account, while the stored definition still showed the restriction.
//
// This gate is the structural answer: adding a field to Spec now
// forces a choice about the wire type instead of leaving the omission
// invisible. It is the same shape as the schema-parity gate in
// internal/tools and the tool-catalog gate — make the silent failure
// loud at build time.
func TestSpecJSONCarriesEverySpecField(t *testing.T) {
	t.Parallel()

	wire := make(map[string]bool)
	for _, name := range jsonWireNames(reflect.TypeOf(specJSON{})) {
		wire[name] = true
	}

	for _, field := range jsonWireNames(reflect.TypeOf(Spec{})) {
		if reason, deliberate := specFieldsNotPersisted[field]; deliberate {
			if reason == "" {
				t.Errorf("Spec field %q is excluded from the wire type with no reason recorded", field)
			}
			continue
		}
		if !wire[field] {
			t.Errorf("Spec field %q has no counterpart in specJSON — it will be silently dropped when a definition is persisted or authored as JSON, and the loop will come back without it. Add it to specJSON and to both conversion directions, or record it in specFieldsNotPersisted with the reason it must not round-trip", field)
		}
	}
}

// specFieldsNotPersisted records Spec fields deliberately absent from
// the JSON wire type, and why. The map exists so that omitting a field
// is a decision someone wrote down rather than something nobody
// noticed.
var specFieldsNotPersisted = map[string]string{}

// TestSpecJSONRoundTripPreservesEveryPopulatedField is the behavioral
// half of the gate above. Name parity proves the field exists on the
// wire type; it does not prove either conversion direction actually
// copies it, which is a second place the same silent drop can happen.
//
// Rather than enumerate fields by hand, this fills the ones that are
// cheap to populate generically and asserts they survive. It is
// deliberately not exhaustive — the name-parity gate is the exhaustive
// one — but it covers the map and slice fields where a forgotten copy
// is easiest to miss.
func TestSpecJSONRoundTripPreservesEveryPopulatedField(t *testing.T) {
	t.Parallel()

	original := Spec{
		Name:         "watcher",
		Operation:    OperationService,
		Task:         "watch",
		Tags:         []string{"forge"},
		ExcludeTools: []string{"shell_exec"},
		Bindings:     map[string]string{BindingForgeAccount: "github-readonly"},
		Metadata:     map[string]string{"category": "service"},
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var data Spec
	if err := json.Unmarshal(encoded, &data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got := data.Bindings[BindingForgeAccount]; got != "github-readonly" {
		t.Errorf("Bindings[%q] = %q, want %q", BindingForgeAccount, got, "github-readonly")
	}
	if len(data.Tags) != 1 || data.Tags[0] != "forge" {
		t.Errorf("Tags = %v, want [forge]", data.Tags)
	}
	if len(data.ExcludeTools) != 1 || data.ExcludeTools[0] != "shell_exec" {
		t.Errorf("ExcludeTools = %v, want [shell_exec]", data.ExcludeTools)
	}
	if data.Metadata["category"] != "service" {
		t.Errorf("Metadata = %v, want category=service", data.Metadata)
	}
}
