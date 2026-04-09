package documents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/database"
)

func TestStoreWriteAndReadManagedDocument(t *testing.T) {
	t.Parallel()

	store, kbDir := newMutationStore(t)
	ctx := context.Background()

	result, err := store.Write(ctx, WriteArgs{
		Ref:         "kb:notes/intro.md",
		Title:       "Intro",
		Description: "Welcome note",
		Tags:        []string{"meta", "index"},
		Body:        stringPtr("# Intro\n\nHelpful body."),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if result.Ref != "kb:notes/intro.md" || result.Path != "notes/intro.md" || result.Existed {
		t.Fatalf("Write result = %#v, want new notes/intro.md document", result)
	}

	raw, err := os.ReadFile(filepath.Join(kbDir, "notes", "intro.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(raw), `tags: ["index", "meta"]`) {
		t.Fatalf("written document = %s, want inline tag array", string(raw))
	}

	record, err := store.Read(ctx, "kb:notes/intro.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if record.Title != "Intro" || record.Description != "Welcome note" {
		t.Fatalf("Read record = %#v, want title/description preserved", record)
	}
	if !strings.Contains(record.Body, "Helpful body.") {
		t.Fatalf("Read body = %q, want helpful body", record.Body)
	}
}

func TestStoreEditUpsertSectionPreservesCreated(t *testing.T) {
	t.Parallel()

	store, _ := newMutationStore(t)
	ctx := context.Background()

	_, err := store.Write(ctx, WriteArgs{
		Ref:   "kb:system.md",
		Title: "System",
		Body:  stringPtr("# System\n\nBase body."),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	before, err := store.Read(ctx, "kb:system.md")
	if err != nil {
		t.Fatalf("Read before edit: %v", err)
	}

	result, err := store.Edit(ctx, EditArgs{
		Ref:     "kb:system.md",
		Mode:    "upsert_section",
		Section: "Observations",
		Content: "Fresh note.",
	})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if result.Section != "Observations" {
		t.Fatalf("Edit result = %#v, want section Observations", result)
	}

	after, err := store.Read(ctx, "kb:system.md")
	if err != nil {
		t.Fatalf("Read after edit: %v", err)
	}
	if firstValue(before.Frontmatter, "created") == "" || firstValue(before.Frontmatter, "created") != firstValue(after.Frontmatter, "created") {
		t.Fatalf("created timestamp changed: before=%q after=%q", firstValue(before.Frontmatter, "created"), firstValue(after.Frontmatter, "created"))
	}
	if !strings.Contains(after.Body, "## Observations") || !strings.Contains(after.Body, "Fresh note.") {
		t.Fatalf("edited body = %q, want inserted section", after.Body)
	}
}

func TestStoreJournalUpdatePrunesOldWindows(t *testing.T) {
	t.Parallel()

	store, _ := newMutationStore(t)
	ctx := context.Background()

	_, err := store.Write(ctx, WriteArgs{
		Ref:   "kb:metacog/journal.md",
		Title: "Metacog Journal",
		Body: stringPtr(strings.Join([]string{
			"## 2000-01-01",
			"",
			"- old one",
			"",
			"## 2000-01-02",
			"",
			"- old two",
		}, "\n")),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	result, err := store.JournalUpdate(ctx, JournalUpdateArgs{
		Ref:        "kb:metacog/journal.md",
		Entry:      "Newest note",
		Window:     "day",
		MaxWindows: 2,
	})
	if err != nil {
		t.Fatalf("JournalUpdate: %v", err)
	}
	if result.Window != "day" {
		t.Fatalf("JournalUpdate result = %#v, want day window", result)
	}

	record, err := store.Read(ctx, "kb:metacog/journal.md")
	if err != nil {
		t.Fatalf("Read after journal update: %v", err)
	}
	if strings.Contains(record.Body, "## 2000-01-01") {
		t.Fatalf("journal body = %q, want oldest window pruned", record.Body)
	}
	if !strings.Contains(record.Body, "Newest note") {
		t.Fatalf("journal body = %q, want newest note appended", record.Body)
	}
	if count := strings.Count(record.Body, "## "); count != 2 {
		t.Fatalf("journal body = %q, want exactly 2 retained windows", record.Body)
	}
}

func newMutationStore(t *testing.T) (*Store, string) {
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
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store, kbDir
}

func TestStoreWritePreservesOrClearsBodyByIntent(t *testing.T) {
	t.Parallel()

	store, _ := newMutationStore(t)
	ctx := context.Background()

	_, err := store.Write(ctx, WriteArgs{
		Ref:   "kb:notes/body.md",
		Title: "Body",
		Body:  stringPtr("Original body."),
	})
	if err != nil {
		t.Fatalf("initial Write: %v", err)
	}
	before, err := store.Read(ctx, "kb:notes/body.md")
	if err != nil {
		t.Fatalf("Read after initial Write: %v", err)
	}

	_, err = store.Write(ctx, WriteArgs{
		Ref:   "kb:notes/body.md",
		Title: "Body Renamed",
	})
	if err != nil {
		t.Fatalf("metadata-only Write: %v", err)
	}
	record, err := store.Read(ctx, "kb:notes/body.md")
	if err != nil {
		t.Fatalf("Read after metadata-only Write: %v", err)
	}
	if record.Body != before.Body {
		t.Fatalf("body after omitted-body write = %q, want %q preserved", record.Body, before.Body)
	}

	_, err = store.Write(ctx, WriteArgs{
		Ref:  "kb:notes/body.md",
		Body: stringPtr(""),
	})
	if err != nil {
		t.Fatalf("clear-body Write: %v", err)
	}
	record, err = store.Read(ctx, "kb:notes/body.md")
	if err != nil {
		t.Fatalf("Read after clear-body Write: %v", err)
	}
	if record.Body != "" {
		t.Fatalf("body after explicit empty-body write = %q, want empty body", record.Body)
	}
}

func stringPtr(s string) *string {
	return &s
}
