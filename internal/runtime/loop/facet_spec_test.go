package loop

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestFacetSpecDecodesBothShapes pins the sugar. The common declaration
// is a bare name, and an author should only reach for the object form on
// the one facet that needs an attribute — otherwise every spec pays for
// a field almost none of them set.
func TestFacetSpecDecodesBothShapes(t *testing.T) {
	var got []FacetSpec
	if err := json.Unmarshal([]byte(`["signal",{"name":"teaser","format":"plain"}]`), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("facets = %d, want 2", len(got))
	}
	if got[0].Name != OutputFacetSignal || got[0].Format != "" {
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
		{Name: OutputFacetSignal},
		{Name: OutputFacetDigest, Format: FacetFormatJSON},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `["signal",{"name":"digest","format":"json"}]`; string(data) != want {
		t.Errorf("marshal = %s, want %s", data, want)
	}
}

// TestFacetFormatValidation covers the enum and the one format that can
// actually be checked.
func TestFacetFormatValidation(t *testing.T) {
	out := OutputSpec{
		Name: "status", Type: OutputTypeMaintainedDocument, Ref: "kb:s.md",
		Facets: []FacetSpec{{Name: OutputFacetSignal, Format: "yaml"}},
	}
	if err := out.Validate(); err == nil {
		t.Error("an unsupported format should be refused at declaration")
	}

	out.Facets = []FacetSpec{{Name: OutputFacetSignal, Format: FacetFormatJSON}}
	if err := out.Validate(); err != nil {
		t.Fatalf("json is a supported format: %v", err)
	}

	// json is the one format a consumer cannot recover from being wrong
	// about, so it is the one enforced at publish.
	err := out.ValidateFacetPayload(FacetPayload{Signal: `not json`, Full: "body"})
	if err == nil {
		t.Fatal("prose in a json facet should be refused")
	}
	if err := out.ValidateFacetPayload(FacetPayload{Signal: `{"ok":true}`, Full: "body"}); err != nil {
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

// TestJSONFacetRoundTripsThroughTheDocument is the guarantee that lets a
// structured facet live in the maintained document rather than beside
// it: rendered as a fenced block a reader can scan, parsed back as the
// value that was published.
func TestJSONFacetRoundTripsThroughTheDocument(t *testing.T) {
	out := OutputSpec{
		Name: "lake", Type: OutputTypeMaintainedDocument, Ref: "kb:lake.md",
		Facets: []FacetSpec{
			{Name: OutputFacetSignal},
			{Name: OutputFacetDigest, Format: FacetFormatJSON},
		},
	}
	payload := FacetPayload{
		Signal: "Canyon Lake 891.2 ft, down 0.3 this week.",
		Digest: `{"level_ft":891.2,"trend":"falling","pct_full":0.62}`,
		Full:   "# Canyon Lake\n\nThe reservoir is falling steadily.",
	}

	body := out.RenderFacetDocument(payload)
	if !strings.Contains(body, "```json") {
		t.Fatalf("a json facet should render inside a fence:\n%s", body)
	}
	if strings.Contains(body, "```json\n{\"level_ft\":891.2,\"trend\":\"falling\",\"pct_full\":0.62}\n```") == false {
		t.Errorf("fenced block malformed:\n%s", body)
	}

	got := out.ParseFacetDocument(body)
	if got.Digest != payload.Digest {
		t.Errorf("digest did not survive the round trip:\n got %q\nwant %q", got.Digest, payload.Digest)
	}
	if got.Signal != payload.Signal {
		t.Errorf("prose facet changed: %q", got.Signal)
	}
	// The parsed value is the JSON that was published, so a consumer can
	// decode it without knowing it passed through markdown.
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got.Digest), &decoded); err != nil {
		t.Fatalf("round-tripped value is not JSON: %v", err)
	}
	if decoded["level_ft"] != 891.2 {
		t.Errorf("level_ft = %v, want 891.2 — the number survived as a number", decoded["level_ft"])
	}
}

// TestJSONFacetRoundTripsPrettyPrinted covers the multi-line case. The
// fence has to survive a value that spans lines, because a payload a
// human is meant to read in the document usually will.
func TestJSONFacetRoundTripsPrettyPrinted(t *testing.T) {
	// digest, not signal: signal is SingleLine, so a multi-line
	// payload there would be refused at publish and this would be testing
	// a call that cannot happen.
	out := OutputSpec{
		Name: "bays", Type: OutputTypeMaintainedDocument, Ref: "kb:b.md",
		Facets: []FacetSpec{
			{Name: OutputFacetSignal},
			{Name: OutputFacetDigest, Format: FacetFormatJSON},
		},
	}
	value := "{\n  \"bay_1\": \"empty\",\n  \"bay_2\": \"NC Miata\",\n  \"bay_3\": \"truck\"\n}"
	payload := FacetPayload{Signal: "Bays 2 and 3 occupied.", Digest: value, Full: "body"}

	// The publish path has to accept it, or the round trip is academic.
	if err := out.ValidateFacetPayload(payload); err != nil {
		t.Fatalf("payload should be publishable: %v", err)
	}
	got := out.ParseFacetDocument(out.RenderFacetDocument(payload))
	if got.Digest != value {
		t.Errorf("multi-line value did not survive:\n got %q\nwant %q", got.Digest, value)
	}
}

// TestMarkdownFacetKeepsItsOwnFence covers what made the first unfence
// wrong: a prose facet may legitimately contain a fenced JSON example,
// and eating that fence would silently change what the author wrote.
func TestMarkdownFacetKeepsItsOwnFence(t *testing.T) {
	out := OutputSpec{
		Name: "doc", Type: OutputTypeMaintainedDocument, Ref: "kb:d.md",
		Facets: []FacetSpec{{Name: OutputFacetSignal}, {Name: OutputFacetDigest}},
	}
	example := "```json\n{\"example\": true}\n```"
	payload := FacetPayload{Signal: "One line.", Digest: example, Full: "body"}

	got := out.ParseFacetDocument(out.RenderFacetDocument(payload))
	if got.Digest != example {
		t.Errorf("a markdown facet lost its fence:\n got %q\nwant %q", got.Digest, example)
	}
}
