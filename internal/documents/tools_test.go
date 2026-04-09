package documents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/database"
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
	for _, want := range []string{`"total_size_bytes"`, `"last_modified_at"`, `"top_directories"`, `"recent_documents"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("Roots() = %s, want field %s", got, want)
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
	_, err = tools.Section(context.Background(), SectionArgs{Ref: "kb:large.md"})
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("Section() error = %v, want size cap failure", err)
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
