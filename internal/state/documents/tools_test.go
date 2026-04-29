package documents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func TestToolsRootsOmitAbsolutePath(t *testing.T) {
	t.Parallel()

	tools := newDocumentToolsTestFixture(t)
	got, err := tools.Roots(context.Background())
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	var payload struct {
		Roots []map[string]any `json:"roots"`
	}
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("json.Unmarshal(Roots()) error: %v", err)
	}
	if len(payload.Roots) == 0 {
		t.Fatalf("Roots() returned no roots: %s", got)
	}
	if _, ok := payload.Roots[0]["path"]; ok {
		t.Fatalf("Roots() leaked root filesystem path: %s", got)
	}
	for _, want := range []string{`"total_size_bytes"`, `"last_modified_delta"`, `"top_directories"`, `"recent_documents"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("Roots() = %s, want field %s", got, want)
		}
	}
	for _, rawTimeField := range []string{`"last_modified_at"`, `"modified_at"`} {
		if strings.Contains(got, rawTimeField) {
			t.Fatalf("Roots() = %s, should not expose raw timestamp field %s", got, rawTimeField)
		}
	}
}

func TestDocumentToolsUseDeltaTimeFields(t *testing.T) {
	t.Parallel()

	tools := newDocumentToolsTestFixture(t)
	ctx := context.Background()

	outputs := map[string]string{}
	var err error
	outputs["roots"], err = tools.Roots(ctx)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	outputs["browse"], err = tools.Browse(ctx, BrowseArgs{Root: "kb"})
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	outputs["search"], err = tools.Search(ctx, SearchArgs{
		Root:            "kb",
		Query:           "note",
		FrontmatterKeys: []string{"created"},
		ModifiedAfter:   "-3600s",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	outputs["read"], err = tools.Read(ctx, RefArgs{Ref: "kb:note.md"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	outputs["write"], err = tools.Write(ctx, WriteArgs{
		Ref:   "kb:written.md",
		Title: "Written",
		Body:  stringPtr("# Written\n\nBody."),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	outputs["read_written"], err = tools.Read(ctx, RefArgs{Ref: "kb:written.md"})
	if err != nil {
		t.Fatalf("Read written: %v", err)
	}
	outputs["values_created"], err = tools.Values(ctx, ValuesArgs{Root: "kb", Key: "created"})
	if err != nil {
		t.Fatalf("Values created: %v", err)
	}

	for name, got := range outputs {
		if strings.Contains(got, `"modified_at"`) ||
			strings.Contains(got, `"last_modified_at"`) ||
			strings.Contains(got, `"created_at"`) ||
			strings.Contains(got, `"updated_at"`) ||
			strings.Contains(got, `"checked_at"`) ||
			strings.Contains(got, `"modified_after":`) ||
			strings.Contains(got, `"modified_before":`) ||
			strings.Contains(got, `"created":`) ||
			strings.Contains(got, `"updated":`) ||
			strings.Contains(got, `"generated_at":`) {
			t.Fatalf("%s output exposes raw timestamp field: %s", name, got)
		}
		if !strings.Contains(got, `_delta"`) {
			t.Fatalf("%s output = %s, want at least one delta time field", name, got)
		}
	}
}

func TestToolsSectionFailsWhenResultTooLarge(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	largeBody := strings.Repeat("Large section body.\n", 5000)
	writeFile(t, filepath.Join(kbDir, "large.md"), "# Large Document\n\n"+largeBody)

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	tools := NewTools(store)
	got, err := tools.Section(context.Background(), SectionArgs{Ref: "kb:large.md"})
	if err != nil {
		t.Fatalf("Section() error = %v, want truncated preview payload", err)
	}
	if !strings.Contains(got, `"truncated": true`) || !strings.Contains(got, `"preview":`) {
		t.Fatalf("Section() = %s, want truncated preview envelope", got)
	}
}

func newDocumentToolsTestFixture(t *testing.T) *Tools {
	t.Helper()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, filepath.Join(kbDir, "note.md"), `---
tags: [test]
---

# Test Note

Helpful note.
`)
	writeFile(t, filepath.Join(kbDir, "network", "nested.md"), `# Nested Note

Helpful nested note.
`)

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return NewTools(store)
}
