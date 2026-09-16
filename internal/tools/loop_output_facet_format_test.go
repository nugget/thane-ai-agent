package tools

import (
	"slices"
	"strings"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestParseOutputFacetsAcceptsFormats pins the guided path against the
// spec-based one. loop_definition_* and spawn_loop unmarshal straight
// into looppkg.Spec, so they have always accepted {name, format}; a
// projection a program reads must be declarable through thane_loop_create
// too, or the recommended path is the one that cannot express it.
func TestParseOutputFacetsAcceptsFormats(t *testing.T) {
	got, err := parseOutputFacets([]any{
		"status_line",
		map[string]any{"name": "teaser"},
		map[string]any{"name": "digest", "format": "json"},
	})
	if err != nil {
		t.Fatalf("parseOutputFacets: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("parsed %d facets, want 3: %+v", len(got), got)
	}
	if got[0].Name != looppkg.OutputFacetStatusLine || got[0].Format != "" {
		t.Errorf("bare name should carry no explicit format, got %+v", got[0])
	}
	if got[1].Name != looppkg.OutputFacetTeaser || got[1].Format != "" {
		t.Errorf("object without format should carry none, got %+v", got[1])
	}
	if got[2].Name != looppkg.OutputFacetDigest || got[2].Format != looppkg.FacetFormatJSON {
		t.Errorf("digest format = %+v, want json", got[2])
	}
	// A bare name must still mean markdown, not an unset that renders
	// differently from what the schema promises.
	if got[0].EffectiveFormat() != looppkg.FacetFormatMarkdown {
		t.Errorf("bare name EffectiveFormat = %q, want markdown", got[0].EffectiveFormat())
	}
}

// TestParseOutputFacetsRejectsBadInput covers every way a declaration can
// be wrong, and checks the error names the offending element rather than
// describing a symptom.
func TestParseOutputFacetsRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		raw     any
		wantErr string
	}{
		{"unknown facet name", []any{"summary"}, "not a projection"},
		{"unknown format", []any{map[string]any{"name": "digest", "format": "yaml"}}, "not an encoding"},
		{"object without a name", []any{map[string]any{"format": "json"}}, "has no name"},
		{"non-string name", []any{map[string]any{"name": 7}}, "facet names are strings"},
		{"non-string format", []any{map[string]any{"name": "digest", "format": 7}}, "formats are strings"},
		{"element is neither", []any{42}, "output.facets[0] is int"},
		{"not an array", "digest", "must be an array"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseOutputFacets(tt.raw)
			if err == nil {
				t.Fatalf("parseOutputFacets(%v) error = nil, want %q", tt.raw, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

// TestParseOutputFacetsStillTakesBareNames guards the older shape, which
// every existing caller and the create tool's own examples still use.
func TestParseOutputFacetsStillTakesBareNames(t *testing.T) {
	got, err := parseOutputFacets([]string{"status_line", "digest"})
	if err != nil {
		t.Fatalf("parseOutputFacets: %v", err)
	}
	if len(got) != 2 || got[0].Name != looppkg.OutputFacetStatusLine || got[1].Name != looppkg.OutputFacetDigest {
		t.Fatalf("bare-name parse = %+v", got)
	}
	if nilFacets, err := parseOutputFacets(nil); err != nil || nilFacets != nil {
		t.Errorf("parseOutputFacets(nil) = %v, %v; want nil, nil", nilFacets, err)
	}
}

// TestParseOutputFacetsRejectsUnknownKeys covers the gap between the
// declared schema and the handler. Nothing enforces a tool's JSON Schema
// at entry, so additionalProperties: false is a promise the parser has to
// keep itself — otherwise a typo'd key is dropped and the projection
// silently falls back to markdown while the caller believes it asked for
// json.
func TestParseOutputFacetsRejectsUnknownKeys(t *testing.T) {
	_, err := parseOutputFacets([]any{map[string]any{"name": "digest", "formatt": "json"}})
	if err == nil {
		t.Fatal("parseOutputFacets accepted an unknown key; the caller would silently get markdown")
	}
	if !strings.Contains(err.Error(), "formatt") {
		t.Errorf("error does not name the offending key: %v", err)
	}
}

// TestCreateToolSchemaOffersBothFacetForms pins the model-visible half of
// this change. Every parser test here passes with the schema's object
// alternative deleted, so without this the contract models actually read
// is uncovered.
func TestCreateToolSchemaOffersBothFacetForms(t *testing.T) {
	rig := newCurateTestRig(t)

	props := rig.tool.Parameters["properties"].(map[string]any)
	output := props["output"].(map[string]any)["properties"].(map[string]any)
	items := output["facets"].(map[string]any)["items"].(map[string]any)

	alternatives, ok := items["anyOf"].([]map[string]any)
	if !ok || len(alternatives) != 2 {
		t.Fatalf("output.facets.items does not offer two alternatives: %#v", items)
	}

	bare, object := alternatives[0], alternatives[1]
	if bare["type"] != "string" {
		t.Errorf("first alternative is not the bare name form: %#v", bare)
	}
	if object["type"] != "object" {
		t.Fatalf("second alternative is not the object form: %#v", object)
	}
	if additional, present := object["additionalProperties"]; !present || additional != false {
		t.Errorf("object form must refuse unknown keys, got additionalProperties=%v", additional)
	}

	objectProps := object["properties"].(map[string]any)
	formats := objectProps["format"].(map[string]any)["enum"].([]string)
	for _, want := range []string{"markdown", "plain", "json"} {
		if !slices.Contains(formats, want) {
			t.Errorf("format enum missing %q: %v", want, formats)
		}
	}

	// Every name the parser accepts must be offered, in both forms, or a
	// model reading the schema cannot reach a projection that works.
	for _, alternative := range []map[string]any{bare, objectProps["name"].(map[string]any)} {
		names := alternative["enum"].([]string)
		for _, want := range []string{"status_line", "teaser", "digest"} {
			if !slices.Contains(names, want) {
				t.Errorf("facet name enum missing %q: %v", want, names)
			}
		}
	}
}
