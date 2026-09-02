package tools

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// facetedBody renders a document the way a publishing loop would, so
// these tests read what the publish path actually writes rather than a
// hand-typed approximation of it.
func facetedBody(t *testing.T) string {
	t.Helper()
	out := looppkg.OutputSpec{
		Name: "office status",
		Type: looppkg.OutputTypeMaintainedDocument,
		Ref:  "core:office.md",
		Facets: []looppkg.FacetSpec{
			{Name: looppkg.OutputFacetStatusLine},
			{Name: looppkg.OutputFacetDigest},
		},
	}
	payload := looppkg.FacetPayload{
		StatusLine: "Printer online; 2 packages waiting.",
		Digest:     "Two deliveries arrived this morning and both need signing.",
		Full:       "# Office\n\n## Deliveries\n\nTwo packages.",
	}
	if err := out.ValidateFacetPayload(payload); err != nil {
		t.Fatalf("fixture payload is not publishable: %v", err)
	}
	return out.RenderFacetDocument(payload)
}

func TestParseFacetSectionsReadsWithoutASpec(t *testing.T) {
	// A consumer holds the body and not the declaration behind it. The
	// headings are fixed by the contract, so what a document has is
	// answerable from the document alone.
	payload, faceted := looppkg.ParseFacetSections(facetedBody(t))
	if !faceted {
		t.Fatal("faceted body reported as unfaceted")
	}
	if got, ok := payload.FacetByKey("status_line"); !ok || got != "Printer online; 2 packages waiting." {
		t.Errorf("status_line = %q (present=%v)", got, ok)
	}
	if got, ok := payload.FacetByKey("digest"); !ok || !strings.Contains(got, "need signing") {
		t.Errorf("digest = %q (present=%v)", got, ok)
	}
	// Undeclared by the fixture, so absent rather than empty-but-present.
	if got, ok := payload.FacetByKey("teaser"); ok {
		t.Errorf("teaser = %q, want absent", got)
	}
	if got, ok := payload.FacetByKey("full"); !ok || !strings.Contains(got, "Two packages") {
		t.Errorf("full = %q (present=%v)", got, ok)
	}
}

func TestParseFacetSectionsAdoptsAnOrdinaryDocument(t *testing.T) {
	body := "# Notes\n\nSomething written before facets existed."
	payload, faceted := looppkg.ParseFacetSections(body)
	if faceted {
		t.Error("an ordinary document reported as faceted")
	}
	if payload.Full != body {
		t.Errorf("full = %q, want the whole body", payload.Full)
	}
}

// TestFacetLevelReadIsIndependentOfSectionStructure is the contract the
// umbrella locked: agent tooling exposes the facet names, never the
// headings Go renders them under. A caller asking for "status_line" must
// never have to know — or be able to depend on — that the answer lives
// under "## Status Line".
func TestFacetLevelReadIsIndependentOfSectionStructure(t *testing.T) {
	body := facetedBody(t)
	if !strings.Contains(body, "## Status Line") {
		t.Fatal("fixture does not render the section heading this test is about")
	}
	payload, _ := looppkg.ParseFacetSections(body)
	value, ok := payload.FacetByKey("status_line")
	if !ok {
		t.Fatal("status_line not readable by its contract name")
	}
	if strings.Contains(value, "##") {
		t.Errorf("the projection carries its own heading: %q", value)
	}
}

func TestFacetKeysAreTheCanonicalLadder(t *testing.T) {
	want := []string{"status_line", "teaser", "digest", "full"}
	got := looppkg.FacetKeys()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("FacetKeys() = %v, want %v", got, want)
	}
	for _, key := range want {
		if !looppkg.IsFacetKey(key) {
			t.Errorf("IsFacetKey(%q) = false", key)
		}
	}
	if looppkg.IsFacetKey("Status Line") {
		t.Error("IsFacetKey accepts a section heading; levels are contract names, not headings")
	}
}

func TestDocReadAdvertisesTheLevelEnum(t *testing.T) {
	// The enum is generated from the contract, so a level that exists is
	// offered and one that does not cannot be asked for.
	r := NewEmptyRegistry()
	RegisterDocumentTools(r, newFacetTestDocumentTools(t))
	tool := r.Get("doc_read")
	if tool == nil {
		t.Fatal("doc_read is not registered")
	}
	properties, _ := tool.Parameters["properties"].(map[string]any)
	level, ok := properties["level"].(map[string]any)
	if !ok {
		t.Fatalf("doc_read has no level parameter; properties = %v", properties)
	}
	enum, _ := level["enum"].([]string)
	if strings.Join(enum, ",") != strings.Join(looppkg.FacetKeys(), ",") {
		t.Errorf("level enum = %v, want the contract's %v", enum, looppkg.FacetKeys())
	}
	// level is a choice, not an obligation: an unlevelled read is still
	// the whole document.
	required, _ := tool.Parameters["required"].([]string)
	for _, name := range required {
		if name == "level" {
			t.Error("level is required; omitting it must still read the whole document")
		}
	}
}

func TestUnknownLevelNamesTheValidOnes(t *testing.T) {
	_, err := readDocumentFacet(t.Context(), newFacetTestDocumentTools(t), "core:office.md", "summary")
	if err == nil {
		t.Fatal("readDocumentFacet error = nil for an unknown level")
	}
	for _, want := range []string{"summary", "status_line", "digest"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

func decodeFacetRead(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, raw)
	}
	return out
}

// newFacetTestDocumentTools returns a document surface holding one
// faceted document at core:office.md, written through the same publish
// rendering a real loop uses.
func newFacetTestDocumentTools(t *testing.T) *documents.Tools {
	t.Helper()
	coreDir := filepath.Join(t.TempDir(), "core")
	if err := mkdirAllForTest(coreDir); err != nil {
		t.Fatalf("mkdir core: %v", err)
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := documents.NewStore(db, map[string]string{"core": coreDir}, nil)
	if err != nil {
		t.Fatalf("documents.NewStore: %v", err)
	}
	return seedDocumentTools(t, store, facetedBody(t))
}

// newSeededDocumentTools is the same surface holding a caller-supplied
// body, for tests about size rather than about content.
func newSeededDocumentTools(t *testing.T, body string) *documents.Tools {
	t.Helper()
	coreDir := filepath.Join(t.TempDir(), "core")
	if err := mkdirAllForTest(coreDir); err != nil {
		t.Fatalf("mkdir core: %v", err)
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := documents.NewStore(db, map[string]string{"core": coreDir}, nil)
	if err != nil {
		t.Fatalf("documents.NewStore: %v", err)
	}
	return seedDocumentTools(t, store, body)
}

func seedDocumentTools(t *testing.T, store *documents.Store, body string) *documents.Tools {
	t.Helper()
	manifest, _, ok := documentfacets.InferLegacy(body, documents.DocumentWriteToolName)
	if !ok {
		t.Fatal("seed body is not a faceted document")
	}
	if _, err := store.Write(t.Context(), documents.WriteArgs{Ref: "core:office.md", Frontmatter: manifest.Frontmatter(), Body: &body}); err != nil {
		t.Fatalf("seed document: %v", err)
	}
	return documents.NewTools(store)
}

func TestDocReadAtALevelReturnsOnlyThatProjection(t *testing.T) {
	dt := newFacetTestDocumentTools(t)

	raw, err := readDocumentFacet(t.Context(), dt, "core:office.md", "status_line")
	if err != nil {
		t.Fatalf("readDocumentFacet: %v", err)
	}
	result := decodeFacetRead(t, raw)
	if result["content"] != "Printer online; 2 packages waiting." {
		t.Errorf("content = %v, want the status line alone", result["content"])
	}
	if result["faceted"] != true {
		t.Errorf("faceted = %v, want true", result["faceted"])
	}
	if result["write_tool"] != documents.DocumentWriteToolName {
		t.Errorf("write_tool = %v, want %q", result["write_tool"], documents.DocumentWriteToolName)
	}
	// The point of reading at a level is not paying for the rest.
	if strings.Contains(raw, "Two packages") {
		t.Errorf("a status_line read carried the document body:\n%s", raw)
	}

	// Available levels are advertised so the next call is a choice
	// rather than a guess.
	levels, _ := result["levels_available"].([]any)
	var got []string
	for _, level := range levels {
		got = append(got, level.(string))
	}
	if strings.Join(got, ",") != "status_line,digest,full" {
		t.Errorf("levels_available = %v, want the declared ones in ladder order", got)
	}
}

func TestDocReadAtAnUndeclaredLevelSaysWhatIsThere(t *testing.T) {
	// The fixture declares no teaser. That is not an error — the document
	// and the level both exist, this one just has nothing at it — so the
	// answer names what it does have.
	raw, err := readDocumentFacet(t.Context(), newFacetTestDocumentTools(t), "core:office.md", "teaser")
	if err != nil {
		t.Fatalf("readDocumentFacet: %v", err)
	}
	result := decodeFacetRead(t, raw)
	if result["content"] != "" {
		t.Errorf("content = %v, want empty", result["content"])
	}
	note, _ := result["note"].(string)
	for _, want := range []string{"status_line", "digest"} {
		if !strings.Contains(note, want) {
			t.Errorf("note = %q, want it to name the %q level as available", note, want)
		}
	}
}

// TestOversizeFullReadPointsAtACheaperLevel covers the one facet with no
// budget of its own. When full outgrows the tool-result ceiling, the
// useful answer is the ladder it already has — not a byte-truncated
// document, and not the generic advice to pick a section, which is the
// structure a level read exists to hide.
func TestOversizeFullReadPointsAtACheaperLevel(t *testing.T) {
	out := looppkg.OutputSpec{
		Name: "office status", Type: looppkg.OutputTypeMaintainedDocument, Ref: "core:office.md",
		Facets: []looppkg.FacetSpec{{Name: looppkg.OutputFacetStatusLine}, {Name: looppkg.OutputFacetDigest}},
	}
	body := out.RenderFacetDocument(looppkg.FacetPayload{
		StatusLine: "Printer online; 2 packages waiting.",
		Digest:     "Two deliveries arrived and both need signing.",
		Full:       "# Office\n\n" + strings.Repeat("Every delivery, in detail. ", 2000),
	})

	dt := newSeededDocumentTools(t, body)
	raw, err := readDocumentFacet(t.Context(), dt, "core:office.md", "full")
	if err != nil {
		t.Fatalf("readDocumentFacet: %v", err)
	}
	result := decodeFacetRead(t, raw)

	if result["truncated"] != true {
		t.Fatalf("an oversized full read was not reported as truncated: %v", result)
	}
	note, _ := result["note"].(string)
	for _, want := range []string{"status_line", "digest"} {
		if !strings.Contains(note, want) {
			t.Errorf("note = %q, want it to offer the %q level", note, want)
		}
	}
	if strings.Contains(note, "full") && strings.Contains(note, "Read it at full") {
		t.Errorf("note offers full as its own remedy: %q", note)
	}
	// Held to the same ceiling as every other document tool result.
	if len(raw) > documents.MaxToolResultBytes {
		t.Errorf("result is %d bytes, past the %d-byte ceiling", len(raw), documents.MaxToolResultBytes)
	}
}

// TestCheaperLevelsStillReadableOnAnOversizeDocument is the other half:
// the ladder has to actually work on the document the note points at.
func TestCheaperLevelsStillReadableOnAnOversizeDocument(t *testing.T) {
	out := looppkg.OutputSpec{
		Name: "office status", Type: looppkg.OutputTypeMaintainedDocument, Ref: "core:office.md",
		Facets: []looppkg.FacetSpec{{Name: looppkg.OutputFacetStatusLine}},
	}
	body := out.RenderFacetDocument(looppkg.FacetPayload{
		StatusLine: "Printer online; 2 packages waiting.",
		Full:       strings.Repeat("Detail. ", 4000),
	})

	raw, err := readDocumentFacet(t.Context(), newSeededDocumentTools(t, body), "core:office.md", "status_line")
	if err != nil {
		t.Fatalf("readDocumentFacet: %v", err)
	}
	if got := decodeFacetRead(t, raw)["content"]; got != "Printer online; 2 packages waiting." {
		t.Errorf("content = %v, want the status line", got)
	}
}

func TestRefIsNormalizedTheSameWithAndWithoutALevel(t *testing.T) {
	// A whitespace-only ref is empty either way; the level must not
	// decide whether the same argument is valid.
	r := NewEmptyRegistry()
	RegisterDocumentTools(r, newFacetTestDocumentTools(t))
	tool := r.Get("doc_read")
	if tool == nil {
		t.Fatal("doc_read is not registered")
	}
	for _, args := range []map[string]any{
		{"ref": "   "},
		{"ref": "   ", "level": "status_line"},
	} {
		if _, err := tool.Handler(t.Context(), args); err == nil || !strings.Contains(err.Error(), "ref is required") {
			t.Errorf("Handler(%v) error = %v, want \"ref is required\"", args, err)
		}
	}

	// And a padded real ref resolves on both paths rather than on one.
	for _, args := range []map[string]any{
		{"ref": " core:office.md "},
		{"ref": " core:office.md ", "level": "status_line"},
	} {
		if _, err := tool.Handler(t.Context(), args); err != nil {
			t.Errorf("Handler(%v) error = %v, want the padded ref to resolve", args, err)
		}
	}
}
