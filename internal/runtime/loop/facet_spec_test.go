package loop

import (
	"encoding/json"
	"testing"
)

// TestFacetSpecDecodesBothShapes pins the sugar. The common declaration
// is a bare name, and an author should only reach for the object form on
// the one facet that needs an attribute — otherwise every spec pays for
// a field almost none of them set.
func TestFacetSpecDecodesBothShapes(t *testing.T) {
	var got []FacetSpec
	if err := json.Unmarshal([]byte(`["status_line",{"name":"teaser","format":"plain"}]`), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("facets = %d, want 2", len(got))
	}
	if got[0].Name != OutputFacetStatusLine || got[0].Format != "" {
		t.Errorf("bare name decoded as %+v", got[0])
	}
	if got[0].EffectiveFormat() != FacetFormatMarkdown {
		t.Errorf("an undeclared format should resolve to markdown, got %q", got[0].EffectiveFormat())
	}
	if got[1].Name != OutputFacetTeaser || got[1].Format != FacetFormatPlain {
		t.Errorf("object form decoded as %+v", got[1])
	}
}

// TestFacetSpecMarshalsBareNameWhenNoAttributes keeps a round trip from
// inflating every declaration into an object it did not start as.
//
// Named for the absence of attributes rather than for "plain", which is
// a format in this package and would read as the subject of the test.
func TestFacetSpecMarshalsBareNameWhenNoAttributes(t *testing.T) {
	data, err := json.Marshal([]FacetSpec{
		{Name: OutputFacetStatusLine},
		{Name: OutputFacetDigest, Format: FacetFormatJSON},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `["status_line",{"name":"digest","format":"json"}]`; string(data) != want {
		t.Errorf("marshal = %s, want %s", data, want)
	}
}

// TestFacetFormatValidation covers the enum and the one format that can
// actually be checked.
func TestFacetFormatValidation(t *testing.T) {
	out := OutputSpec{
		Name: "status", Type: OutputTypeMaintainedDocument, Ref: "kb:s.md",
		Facets: []FacetSpec{{Name: OutputFacetStatusLine, Format: "yaml"}},
	}
	if err := out.Validate(); err == nil {
		t.Error("an unsupported format should be refused at declaration")
	}

	out.Facets = []FacetSpec{{Name: OutputFacetStatusLine, Format: FacetFormatJSON}}
	if err := out.Validate(); err != nil {
		t.Fatalf("json is a supported format: %v", err)
	}

	// json is the one format a consumer cannot recover from being wrong
	// about, so it is the one enforced at publish.
	err := out.ValidateFacetPayload(FacetPayload{StatusLine: `not json`, Full: "body"})
	if err == nil {
		t.Fatal("prose in a json facet should be refused")
	}
	if err := out.ValidateFacetPayload(FacetPayload{StatusLine: `{"ok":true}`, Full: "body"}); err != nil {
		t.Errorf("valid JSON should pass: %v", err)
	}
}

// TestFacetFormatShapesGuidance checks that a declared format reaches
// the model rather than only the validator — a rule enforced but never
// explained is one the model learns by failing.
func TestFacetFormatShapesGuidance(t *testing.T) {
	if g := FormatGuidance(FacetFormatMarkdown); g != "" {
		t.Errorf("the default format should add nothing, got %q", g)
	}
	for _, f := range []FacetFormat{FacetFormatPlain, FacetFormatJSON} {
		if FormatGuidance(f) == "" {
			t.Errorf("format %q should explain itself to the model", f)
		}
	}
}
