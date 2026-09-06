package tools

import (
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
