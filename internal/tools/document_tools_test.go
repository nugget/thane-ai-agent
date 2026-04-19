package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/documents"
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
