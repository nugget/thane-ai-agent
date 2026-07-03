package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

func TestNumericArgSupportsCommonTypesAndBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   any
		def  int
		max  int
		want int
	}{
		{name: "nil uses default", in: nil, def: 20, max: 100, want: 20},
		{name: "int", in: 12, def: 20, max: 100, want: 12},
		{name: "int64", in: int64(15), def: 20, max: 100, want: 15},
		{name: "float64", in: float64(18), def: 20, max: 100, want: 18},
		{name: "json number", in: json.Number("22"), def: 20, max: 100, want: 22},
		{name: "non-positive uses default", in: 0, def: 20, max: 100, want: 20},
		{name: "clamped", in: 500, def: 20, max: 100, want: 100},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := numericArg(tc.in, tc.def, tc.max); got != tc.want {
				t.Fatalf("numericArg(%v, %d, %d) = %d, want %d", tc.in, tc.def, tc.max, got, tc.want)
			}
		})
	}
}

func TestDocumentFrontmatterArgSupportsStringsAndArrays(t *testing.T) {
	t.Parallel()

	got := documentFrontmatterArg(map[string]any{
		"title": "Notebook",
		"tags":  []any{"alpha", "beta"},
		"blank": "",
		"skip":  []any{1, "ok"},
	})
	want := map[string][]string{
		"title": {"Notebook"},
		"tags":  {"alpha", "beta"},
		"skip":  {"ok"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("documentFrontmatterArg(...) = %#v, want %#v", got, want)
	}
}

func TestDocWriteHandlerPreservesOrClearsBodyByIntent(t *testing.T) {
	t.Parallel()

	reg, store := newTestDocumentRegistry(t)
	writeTool := reg.Get("doc_write")
	if writeTool == nil {
		t.Fatal("doc_write not registered")
	}

	_, err := writeTool.Handler(context.Background(), map[string]any{
		"ref":   "kb:notes/handler.md",
		"title": "Handler",
		"body":  "Original body.",
	})
	if err != nil {
		t.Fatalf("initial doc_write: %v", err)
	}
	before, err := store.Read(context.Background(), "kb:notes/handler.md")
	if err != nil {
		t.Fatalf("Read after initial doc_write: %v", err)
	}

	_, err = writeTool.Handler(context.Background(), map[string]any{
		"ref":   "kb:notes/handler.md",
		"title": "Handler Renamed",
	})
	if err != nil {
		t.Fatalf("metadata-only doc_write: %v", err)
	}
	record, err := store.Read(context.Background(), "kb:notes/handler.md")
	if err != nil {
		t.Fatalf("Read after metadata-only doc_write: %v", err)
	}
	if record.Body != before.Body {
		t.Fatalf("body after omitted-body doc_write = %q, want %q preserved", record.Body, before.Body)
	}

	_, err = writeTool.Handler(context.Background(), map[string]any{
		"ref":  "kb:notes/handler.md",
		"body": "",
	})
	if err != nil {
		t.Fatalf("empty-body doc_write: %v", err)
	}
	record, err = store.Read(context.Background(), "kb:notes/handler.md")
	if err != nil {
		t.Fatalf("Read after empty-body doc_write: %v", err)
	}
	if record.Body != "" {
		t.Fatalf("body after explicit empty-body doc_write = %q, want empty body", record.Body)
	}
}

func TestDocWriteHandlerAppendsJournalEntry(t *testing.T) {
	t.Parallel()

	reg, store := newTestDocumentRegistry(t)
	writeTool := reg.Get("doc_write")
	if writeTool == nil {
		t.Fatal("doc_write not registered")
	}

	_, err := writeTool.Handler(context.Background(), map[string]any{
		"ref":           "kb:notes/journaled.md",
		"body":          "# State\n\nWorking through it.",
		"journal_entry": "Captured the first checkpoint.",
	})
	if err != nil {
		t.Fatalf("doc_write with journal_entry: %v", err)
	}

	record, err := store.Read(context.Background(), "kb:notes/journaled.md")
	if err != nil {
		t.Fatalf("Read after journaled doc_write: %v", err)
	}
	if !strings.Contains(record.Body, "## Journal") {
		t.Fatalf("body = %q, want Journal section", record.Body)
	}
	if !strings.Contains(record.Body, "Captured the first checkpoint.") {
		t.Fatalf("body = %q, want journal entry content", record.Body)
	}
}

func TestDocumentSearchAndLinksHandlersSupportStructuredNavigation(t *testing.T) {
	t.Parallel()

	reg, store := newTestDocumentRegistry(t)

	if _, err := store.Write(context.Background(), documents.WriteArgs{
		Ref:   "kb:network/vlans.md",
		Title: "VLAN Guide",
		Tags:  []string{"network", "vlans"},
		Frontmatter: map[string][]string{
			"status": {"active"},
		},
		Body: stringPtr("# VLAN Guide\n\nReference for the home network VLAN layout.\n"),
	}); err != nil {
		t.Fatalf("store.Write(vlans): %v", err)
	}
	if _, err := store.Write(context.Background(), documents.WriteArgs{
		Ref:   "kb:notes/cameras.md",
		Title: "Camera Notes",
		Body:  stringPtr("# Camera Notes\n\nSee the [trusted VLAN notes](../network/vlans.md#trusted).\n"),
	}); err != nil {
		t.Fatalf("store.Write(cameras): %v", err)
	}

	searchTool := reg.Get("doc_search")
	if searchTool == nil {
		t.Fatal("doc_search not registered")
	}
	searchOut, err := searchTool.Handler(context.Background(), map[string]any{
		"root":             "kb",
		"frontmatter":      map[string]any{"status": "active"},
		"modified_after":   "-3600s",
		"frontmatter_keys": []any{},
	})
	if err != nil {
		t.Fatalf("doc_search: %v", err)
	}
	if !strings.Contains(searchOut, `"ref": "kb:network/vlans.md"`) {
		t.Fatalf("doc_search output = %s, want vlans document", searchOut)
	}

	linksTool := reg.Get("doc_links")
	if linksTool == nil {
		t.Fatal("doc_links not registered")
	}
	linksOut, err := linksTool.Handler(context.Background(), map[string]any{
		"ref":  "kb:network/vlans.md",
		"mode": "backlinks",
	})
	if err != nil {
		t.Fatalf("doc_links: %v", err)
	}
	if !strings.Contains(linksOut, `"ref": "kb:notes/cameras.md"`) || !strings.Contains(linksOut, `"targets": [`) {
		t.Fatalf("doc_links output = %s, want cameras backlink with target list", linksOut)
	}
}

func TestDocumentLinksHandlerSupportsLimitsAndTruncation(t *testing.T) {
	t.Parallel()

	reg, store := newTestDocumentRegistry(t)

	for _, doc := range []documents.WriteArgs{
		{
			Ref:   "kb:network/vlans.md",
			Title: "VLAN Guide",
			Body:  stringPtr("# VLAN Guide\n\nReference for the home network VLAN layout.\n"),
		},
		{
			Ref:   "kb:notes/routers.md",
			Title: "Router Notes",
			Body:  stringPtr("# Router Notes\n\nSee [[VLAN Guide]].\n"),
		},
		{
			Ref:   "kb:notes/switches.md",
			Title: "Switch Notes",
			Body:  stringPtr("# Switch Notes\n\nSee [[VLAN Guide]].\n"),
		},
		{
			Ref:   "kb:notes/cameras.md",
			Title: "Camera Notes",
			Body:  stringPtr("# Camera Notes\n\nSee [trusted](../network/vlans.md#trusted), [iot](../network/vlans.md#iot), and [[VLAN Guide]].\n"),
		},
	} {
		if _, err := store.Write(context.Background(), doc); err != nil {
			t.Fatalf("store.Write(%s): %v", doc.Ref, err)
		}
	}

	linksTool := reg.Get("doc_links")
	if linksTool == nil {
		t.Fatal("doc_links not registered")
	}

	backlinksOut, err := linksTool.Handler(context.Background(), map[string]any{
		"ref":                "kb:network/vlans.md",
		"mode":               "backlinks",
		"limit":              2,
		"per_backlink_limit": 1,
	})
	if err != nil {
		t.Fatalf("doc_links(backlinks): %v", err)
	}

	var backlinks documents.LinksResult
	if err := json.Unmarshal([]byte(backlinksOut), &backlinks); err != nil {
		t.Fatalf("unmarshal backlinks output: %v", err)
	}
	if backlinks.Ref != "kb:network/vlans.md" || backlinks.Limit != 2 || backlinks.PerBacklinkLimit != 1 {
		t.Fatalf("backlinks result = %#v, want canonical ref and echoed limits", backlinks)
	}
	if len(backlinks.Backlinks) != 2 || !backlinks.BacklinksTruncated {
		t.Fatalf("backlinks = %#v, want 2 truncated backlink sources", backlinks.Backlinks)
	}
	foundCameras := false
	for _, backlink := range backlinks.Backlinks {
		if backlink.Ref != "kb:notes/cameras.md" {
			continue
		}
		foundCameras = true
		if len(backlink.Targets) != 1 || !backlink.TargetsTruncated {
			t.Fatalf("camera backlink = %#v, want 1 truncated target", backlink)
		}
	}
	if !foundCameras {
		t.Fatalf("backlinks = %#v, want cameras backlink present", backlinks.Backlinks)
	}

	outgoingOut, err := linksTool.Handler(context.Background(), map[string]any{
		"ref":   "kb:notes/cameras.md",
		"mode":  "outgoing",
		"limit": 2,
	})
	if err != nil {
		t.Fatalf("doc_links(outgoing): %v", err)
	}

	var outgoing documents.LinksResult
	if err := json.Unmarshal([]byte(outgoingOut), &outgoing); err != nil {
		t.Fatalf("unmarshal outgoing output: %v", err)
	}
	if len(outgoing.Outgoing) != 2 || !outgoing.OutgoingTruncated {
		t.Fatalf("outgoing = %#v, want 2 truncated outgoing links", outgoing.Outgoing)
	}
}

func TestDocumentIntakeAndCommitHandlersCreateManagedDocument(t *testing.T) {
	t.Parallel()

	reg, store := newTestDocumentRegistry(t)
	intakeTool := reg.Get("doc_intake")
	if intakeTool == nil {
		t.Fatal("doc_intake not registered")
	}
	commitTool := reg.Get("doc_commit")
	if commitTool == nil {
		t.Fatal("doc_commit not registered")
	}

	intakeOut, err := intakeTool.Handler(context.Background(), map[string]any{
		"root":          "kb",
		"intent":        "create a durable operating note",
		"summary":       "Garage door opener reset notes.",
		"desired_title": "Garage Door Reset",
		"tags":          []any{"home"},
	})
	if err != nil {
		t.Fatalf("doc_intake: %v", err)
	}
	var intake documents.IntakeResult
	if err := json.Unmarshal([]byte(intakeOut), &intake); err != nil {
		t.Fatalf("unmarshal doc_intake: %v", err)
	}
	if intake.IntakeID == "" || intake.ProposedRef == "" {
		t.Fatalf("intake = %#v, want id and proposed ref", intake)
	}

	if _, err := commitTool.Handler(context.Background(), map[string]any{
		"intake_id": intake.IntakeID,
		"body":      "# Garage Door Reset\n\nHold the wall button until the opener resets.",
	}); err != nil {
		t.Fatalf("doc_commit: %v", err)
	}
	record, err := store.Read(context.Background(), intake.ProposedRef)
	if err != nil {
		t.Fatalf("Read committed intake document: %v", err)
	}
	if record.Title != "Garage Door Reset" {
		t.Fatalf("record title = %q, want Garage Door Reset", record.Title)
	}
}

func newTestDocumentRegistry(t *testing.T) (*Registry, *documents.Store) {
	t.Helper()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := documents.NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("documents.NewStore: %v", err)
	}

	reg := NewEmptyRegistry()
	RegisterDocumentTools(reg, documents.NewTools(store))
	return reg, store
}

func stringPtr(s string) *string {
	return &s
}

// The prod incident (2026-07-02): the archivist model called doc_write
// with doc_edit's vocabulary — {"content": <full dossier>, "mode":
// "replace_body", "ref": ...}. The unknown keys were silently ignored,
// an empty document was created, and success was returned; three
// dossiers' content survived only in the tool-call log. These guards
// turn that silent data loss into a self-correcting error.
func TestDocWriteRejectsDocEditVocabulary(t *testing.T) {
	t.Parallel()

	reg, store := newTestDocumentRegistry(t)
	writeTool := reg.Get("doc_write")

	// The exact prod argument shape.
	_, err := writeTool.Handler(context.Background(), map[string]any{
		"ref":     "kb:dossiers/entity-binary_sensor-zone25_garage_bay_3.md",
		"content": "# Dossier: full content that must not be lost",
		"mode":    "replace_body",
	})
	if err == nil {
		t.Fatal("doc_write with content+mode succeeded; want a redirect error")
	}
	if !strings.Contains(err.Error(), "body") {
		t.Errorf("error should teach the body parameter: %v", err)
	}
	// Nothing may be created by the failed call.
	if _, readErr := store.Read(context.Background(), "kb:dossiers/entity-binary_sensor-zone25_garage_bay_3.md"); readErr == nil {
		t.Error("failed doc_write still created a document")
	}

	// mode alone (even with a valid body) is doc_edit vocabulary.
	_, err = writeTool.Handler(context.Background(), map[string]any{
		"ref":  "kb:notes/mode-guard.md",
		"body": "real body",
		"mode": "append_body",
	})
	if err == nil || !strings.Contains(err.Error(), "doc_edit") {
		t.Errorf("doc_write with mode should redirect to doc_edit, got: %v", err)
	}
}

// doc_edit's text parameter is unified with doc_write's as body; the
// legacy content key gets a rename-teaching error instead of being
// silently ignored (a model replaying old history would otherwise
// apply an edit with empty text).
func TestDocEditTakesBodyAndTeachesContentRename(t *testing.T) {
	t.Parallel()

	reg, store := newTestDocumentRegistry(t)
	writeTool := reg.Get("doc_write")
	editTool := reg.Get("doc_edit")

	if _, err := writeTool.Handler(context.Background(), map[string]any{
		"ref":  "kb:notes/unified.md",
		"body": "original",
	}); err != nil {
		t.Fatalf("seed doc_write: %v", err)
	}

	// The unified vocabulary: body works on doc_edit.
	if _, err := editTool.Handler(context.Background(), map[string]any{
		"ref":  "kb:notes/unified.md",
		"mode": "replace_body",
		"body": "replaced via body",
	}); err != nil {
		t.Fatalf("doc_edit with body: %v", err)
	}
	record, err := store.Read(context.Background(), "kb:notes/unified.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if strings.TrimSpace(record.Body) != "replaced via body" {
		t.Fatalf("body = %q, want replacement applied", record.Body)
	}

	// The legacy key teaches the rename and applies nothing.
	_, err = editTool.Handler(context.Background(), map[string]any{
		"ref":     "kb:notes/unified.md",
		"mode":    "replace_body",
		"content": "must not be applied",
	})
	if err == nil || !strings.Contains(err.Error(), "renamed") {
		t.Errorf("doc_edit with content should teach the rename, got: %v", err)
	}
	record, err = store.Read(context.Background(), "kb:notes/unified.md")
	if err != nil {
		t.Fatalf("Read after rejected edit: %v", err)
	}
	if strings.TrimSpace(record.Body) != "replaced via body" {
		t.Errorf("rejected edit still mutated the document: %q", record.Body)
	}
}

// Creating a new document requires body: an omitted body means
// "preserve", which is meaningless on a create and is the signature of
// arguments that missed the schema. An explicit empty string stays the
// documented way to create a blank document.
func TestDocWriteCreateRequiresBody(t *testing.T) {
	t.Parallel()

	reg, store := newTestDocumentRegistry(t)
	writeTool := reg.Get("doc_write")

	_, err := writeTool.Handler(context.Background(), map[string]any{
		"ref":   "kb:notes/bodiless-create.md",
		"title": "Bodiless",
	})
	if err == nil || !strings.Contains(err.Error(), "requires body") {
		t.Fatalf("bodiless create should error, got: %v", err)
	}
	if _, readErr := store.Read(context.Background(), "kb:notes/bodiless-create.md"); readErr == nil {
		t.Error("failed bodiless create still produced a document")
	}

	// Explicit empty string: intentional blank create still works.
	if _, err := writeTool.Handler(context.Background(), map[string]any{
		"ref":  "kb:notes/intentional-blank.md",
		"body": "",
	}); err != nil {
		t.Fatalf("explicit empty-body create should succeed: %v", err)
	}
}

// doc_create is the default create verb (#1038): one call collision-
// checks and writes. It carries the unified body vocabulary (#1201).
func TestDocCreateHandlerWritesAndGuardsVocabulary(t *testing.T) {
	t.Parallel()

	reg, store := newTestDocumentRegistry(t)
	createTool := reg.Get("doc_create")
	if createTool == nil {
		t.Fatal("doc_create not registered")
	}

	out, err := createTool.Handler(context.Background(), map[string]any{
		"root":  "kb",
		"title": "Fence Charger Notes",
		"body":  "# Fence Charger Notes\n\nSouth paddock charger reads 7.2kV after the storm.",
	})
	if err != nil {
		t.Fatalf("doc_create: %v", err)
	}
	var commit struct {
		Ref    string `json:"ref"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &commit); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if commit.Status != "committed" || commit.Ref == "" {
		t.Fatalf("result = %s, want committed with ref", out)
	}
	if _, err := store.Read(context.Background(), commit.Ref); err != nil {
		t.Fatalf("created doc unreadable: %v", err)
	}

	// The unified vocabulary holds here too.
	_, err = createTool.Handler(context.Background(), map[string]any{
		"root":    "kb",
		"content": "markdown in the wrong parameter",
	})
	if err == nil || !strings.Contains(err.Error(), "body") {
		t.Errorf("doc_create with content should teach body, got: %v", err)
	}
}
