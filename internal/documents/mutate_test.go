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

func TestStoreDeleteRemovesManagedDocument(t *testing.T) {
	t.Parallel()

	store, kbDir := newMutationStore(t)
	ctx := context.Background()

	_, err := store.Write(ctx, WriteArgs{
		Ref:   "kb:notes/delete-me.md",
		Title: "Delete Me",
		Body:  stringPtr("Gone soon."),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	result, err := store.Delete(ctx, DeleteArgs{Ref: "kb:notes/delete-me.md"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if result.DeletedRef != "kb:notes/delete-me.md" {
		t.Fatalf("Delete result = %#v, want deleted ref", result)
	}

	if _, err := os.Stat(filepath.Join(kbDir, "notes", "delete-me.md")); !os.IsNotExist(err) {
		t.Fatalf("deleted file stat err = %v, want not exist", err)
	}
	if _, err := store.Read(ctx, "kb:notes/delete-me.md"); err == nil || !strings.Contains(err.Error(), "document not found") {
		t.Fatalf("Read after Delete error = %v, want document not found", err)
	}
}

func TestStoreMoveRenamesManagedDocument(t *testing.T) {
	t.Parallel()

	store, kbDir := newMutationStore(t)
	ctx := context.Background()

	_, err := store.Write(ctx, WriteArgs{
		Ref:   "kb:notes/source.md",
		Title: "Source",
		Body:  stringPtr("Move this."),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	result, err := store.Move(ctx, MoveArgs{
		Ref:            "kb:notes/source.md",
		DestinationRef: "kb:archive/moved.md",
	})
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if result.ToRef != "kb:archive/moved.md" || result.FromRef != "kb:notes/source.md" {
		t.Fatalf("Move result = %#v, want moved refs", result)
	}

	if _, err := store.Read(ctx, "kb:notes/source.md"); err == nil || !strings.Contains(err.Error(), "document not found") {
		t.Fatalf("Read source after Move error = %v, want document not found", err)
	}
	record, err := store.Read(ctx, "kb:archive/moved.md")
	if err != nil {
		t.Fatalf("Read moved doc: %v", err)
	}
	if !strings.Contains(record.Body, "Move this.") {
		t.Fatalf("moved body = %q, want preserved content", record.Body)
	}
	if _, err := os.Stat(filepath.Join(kbDir, "archive", "moved.md")); err != nil {
		t.Fatalf("moved file stat err = %v", err)
	}
}

func TestStoreCopyDuplicatesManagedDocument(t *testing.T) {
	t.Parallel()

	store, kbDir := newMutationStore(t)
	ctx := context.Background()

	_, err := store.Write(ctx, WriteArgs{
		Ref:   "kb:notes/original.md",
		Title: "Original",
		Body:  stringPtr("Copy this."),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	result, err := store.Copy(ctx, CopyArgs{
		Ref:            "kb:notes/original.md",
		DestinationRef: "kb:notes/copied.md",
	})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if result.ToRef != "kb:notes/copied.md" || result.FromRef != "kb:notes/original.md" {
		t.Fatalf("Copy result = %#v, want copied refs", result)
	}

	original, err := store.Read(ctx, "kb:notes/original.md")
	if err != nil {
		t.Fatalf("Read original: %v", err)
	}
	copied, err := store.Read(ctx, "kb:notes/copied.md")
	if err != nil {
		t.Fatalf("Read copied: %v", err)
	}
	if original.Body != copied.Body {
		t.Fatalf("copied body = %q, want %q", copied.Body, original.Body)
	}
	if _, err := os.Stat(filepath.Join(kbDir, "notes", "copied.md")); err != nil {
		t.Fatalf("copied file stat err = %v", err)
	}
}

func TestStoreCopySectionCreatesDestinationDocument(t *testing.T) {
	t.Parallel()

	store, _ := newMutationStore(t)
	ctx := context.Background()

	_, err := store.Write(ctx, WriteArgs{
		Ref: "kb:source.md",
		Body: stringPtr(strings.Join([]string{
			"# Source",
			"",
			"## Ideas",
			"",
			"Alpha idea.",
			"",
			"## Notes",
			"",
			"Keep me here.",
		}, "\n")),
	})
	if err != nil {
		t.Fatalf("Write source: %v", err)
	}

	result, err := store.CopySection(ctx, SectionTransferArgs{
		Ref:                "kb:source.md",
		Section:            "Ideas",
		DestinationRef:     "kb:ideas.md",
		DestinationSection: "Copied Ideas",
		DestinationLevel:   3,
	})
	if err != nil {
		t.Fatalf("CopySection: %v", err)
	}
	if result.DestinationSection != "Copied Ideas" || result.DestinationLevel != 3 {
		t.Fatalf("CopySection result = %#v, want renamed section at level 3", result)
	}

	source, err := store.Read(ctx, "kb:source.md")
	if err != nil {
		t.Fatalf("Read source: %v", err)
	}
	if !strings.Contains(source.Body, "## Ideas") {
		t.Fatalf("source body = %q, want original section preserved", source.Body)
	}

	destination, err := store.Read(ctx, "kb:ideas.md")
	if err != nil {
		t.Fatalf("Read destination: %v", err)
	}
	if !strings.Contains(destination.Body, "### Copied Ideas") || !strings.Contains(destination.Body, "Alpha idea.") {
		t.Fatalf("destination body = %q, want copied section content", destination.Body)
	}
}

func TestStoreMoveSectionRemovesSourceSection(t *testing.T) {
	t.Parallel()

	store, _ := newMutationStore(t)
	ctx := context.Background()

	_, err := store.Write(ctx, WriteArgs{
		Ref: "kb:source.md",
		Body: stringPtr(strings.Join([]string{
			"# Source",
			"",
			"## Move Me",
			"",
			"Shift this section.",
			"",
			"## Keep Me",
			"",
			"Still here.",
		}, "\n")),
	})
	if err != nil {
		t.Fatalf("Write source: %v", err)
	}

	result, err := store.MoveSection(ctx, SectionTransferArgs{
		Ref:            "kb:source.md",
		Section:        "Move Me",
		DestinationRef: "kb:dest.md",
	})
	if err != nil {
		t.Fatalf("MoveSection: %v", err)
	}
	if result.SourceSection != "Move Me" || result.DestinationSection != "Move Me" {
		t.Fatalf("MoveSection result = %#v, want moved section metadata", result)
	}

	source, err := store.Read(ctx, "kb:source.md")
	if err != nil {
		t.Fatalf("Read source: %v", err)
	}
	if strings.Contains(source.Body, "## Move Me") {
		t.Fatalf("source body = %q, want moved section removed", source.Body)
	}
	if !strings.Contains(source.Body, "## Keep Me") {
		t.Fatalf("source body = %q, want remaining sections preserved", source.Body)
	}

	destination, err := store.Read(ctx, "kb:dest.md")
	if err != nil {
		t.Fatalf("Read destination: %v", err)
	}
	if !strings.Contains(destination.Body, "## Move Me") || !strings.Contains(destination.Body, "Shift this section.") {
		t.Fatalf("destination body = %q, want moved section content", destination.Body)
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
