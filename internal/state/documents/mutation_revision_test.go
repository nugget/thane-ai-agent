package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

type revisionMutationBackend struct {
	root     string
	revision string
	next     int
	contents map[string]string
}

func (b *revisionMutationBackend) Write(_ context.Context, filename, content, _ string) error {
	return b.write(filename, content)
}

func (b *revisionMutationBackend) WriteIfRevision(_ context.Context, filename, content, _ string, expectedRevision string) (string, error) {
	actual := b.revision
	if actual == "" {
		actual = "absent"
	}
	if expectedRevision != actual {
		return "", &RootRevisionConflictError{Expected: expectedRevision, Actual: actual}
	}
	if err := b.write(filename, content); err != nil {
		return "", err
	}
	return b.revision, nil
}

func (b *revisionMutationBackend) Delete(_ context.Context, filename, _ string) error {
	return os.Remove(filepath.Join(b.root, filename))
}

func (b *revisionMutationBackend) Resolve(_ context.Context, _ string, _ string) (RevisionRef, error) {
	if b.revision == "" {
		return RevisionRef{}, fmt.Errorf("no revision")
	}
	return RevisionRef{Commit: b.revision, Short: b.revision}, nil
}

func (b *revisionMutationBackend) History(context.Context, string, RevisionQuery) (RevisionListing, error) {
	return RevisionListing{}, nil
}

func (b *revisionMutationBackend) Diff(_ context.Context, _ string, from, to, _ string) (RevisionDiff, error) {
	before, beforeOK := b.contents[from]
	after, afterOK := b.contents[to]
	if !beforeOK || !afterOK {
		return RevisionDiff{}, fmt.Errorf("unknown diff endpoint")
	}
	return RevisionDiff{
		Added:   1,
		Removed: 1,
		Body:    fmt.Sprintf("--- a/person.md\n+++ b/person.md\n-%s\n+%s\n", before, after),
	}, nil
}

func (b *revisionMutationBackend) Content(context.Context, string, string) (RevisionContent, error) {
	return RevisionContent{}, nil
}

func (b *revisionMutationBackend) Snapshot(_ context.Context, filename string) (RevisionContent, error) {
	content, err := os.ReadFile(filepath.Join(b.root, filename))
	if err != nil {
		return RevisionContent{}, err
	}
	return RevisionContent{
		Revision: RevisionRef{Commit: b.revision, Short: b.revision},
		Content:  string(content),
	}, nil
}

func (b *revisionMutationBackend) write(filename, content string) error {
	path := filepath.Join(b.root, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	b.next++
	b.revision = fmt.Sprintf("rev-%d", b.next)
	if b.contents == nil {
		b.contents = make(map[string]string)
	}
	b.contents[b.revision] = content
	return nil
}

func TestHiddenRevisionReceiptReturnsConflictDiffAndAdvances(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := &revisionMutationBackend{root: root, contents: make(map[string]string)}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewStoreWithOptions(db, map[string]string{"contacts": root}, nil, StoreOptions{
		RootWriters:  map[string]RootWriter{"contacts": backend},
		RootRevisers: map[string]RootReviser{"contacts": backend},
	})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}
	tools := NewTools(store)
	ctx := t.Context()

	createdJSON, err := tools.Write(ctx, WriteArgs{
		Ref:          "contacts:person.md",
		Body:         stringPtr("First fact."),
		ReceiptScope: "loop:archivist-contacts",
	})
	if err != nil {
		t.Fatalf("Write with absent revision: %v", err)
	}
	var created modelMutationResult
	if err := json.Unmarshal([]byte(createdJSON), &created); err != nil {
		t.Fatalf("unmarshal create result: %v", err)
	}
	if !created.Applied {
		t.Fatalf("create applied = false, want true: %s", createdJSON)
	}
	if strings.Contains(createdJSON, "revision") {
		t.Fatalf("create exposed revision machinery: %s", createdJSON)
	}

	readJSON, err := tools.Read(ctx, RefArgs{Ref: "contacts:person.md", ReceiptScope: "loop:archivist-contacts"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if strings.Contains(readJSON, "revision") {
		t.Fatalf("read exposed revision machinery: %s", readJSON)
	}
	blindJSON, err := tools.Write(ctx, WriteArgs{
		Ref:          "contacts:person.md",
		Body:         stringPtr("Blind replacement."),
		ReceiptScope: "loop:different-writer",
	})
	if err != nil {
		t.Fatalf("blind replacement: %v", err)
	}
	var blind modelMutationConflict
	if err := json.Unmarshal([]byte(blindJSON), &blind); err != nil {
		t.Fatalf("unmarshal blind replacement: %v", err)
	}
	if blind.Applied || !strings.Contains(blind.Message, "Read contacts:person.md first") {
		t.Fatalf("blind replacement = %#v, want read-first refusal", blind)
	}

	if err := backend.Write(ctx, "person.md", "Second fact.", "operator update"); err != nil {
		t.Fatalf("external update: %v", err)
	}
	conflictJSON, err := tools.Edit(ctx, EditArgs{
		Ref:          "contacts:person.md",
		Mode:         "append_body",
		Body:         "Stale fact.",
		ReceiptScope: "loop:archivist-contacts",
	})
	if err != nil {
		t.Fatalf("stale Edit: %v", err)
	}
	var conflict modelMutationConflict
	if err := json.Unmarshal([]byte(conflictJSON), &conflict); err != nil {
		t.Fatalf("unmarshal conflict result: %v", err)
	}
	if conflict.Applied || !strings.Contains(conflict.ChangedSinceRead, "Second fact.") {
		t.Fatalf("conflict = %#v, want unapplied result with intervening diff", conflict)
	}
	if strings.Contains(conflictJSON, "rev-1") || strings.Contains(conflictJSON, "rev-2") {
		t.Fatalf("conflict exposed hidden revision tokens: %s", conflictJSON)
	}
	record, err := store.Read(ctx, "contacts:person.md")
	if err != nil {
		t.Fatalf("Read after stale edit: %v", err)
	}
	if !strings.Contains(record.Body, "Second fact.") || strings.Contains(record.Body, "Stale fact.") {
		t.Fatalf("body after stale edit = %q, want second fact without stale fact", record.Body)
	}

	retryJSON, err := tools.Edit(ctx, EditArgs{
		Ref:          "contacts:person.md",
		Mode:         "append_body",
		Body:         "Reconciled fact.",
		ReceiptScope: "loop:archivist-contacts",
	})
	if err != nil {
		t.Fatalf("retry Edit: %v", err)
	}
	var retried modelMutationResult
	if err := json.Unmarshal([]byte(retryJSON), &retried); err != nil {
		t.Fatalf("unmarshal retry result: %v", err)
	}
	if !retried.Applied {
		t.Fatalf("retry applied = false, want true: %s", retryJSON)
	}
}

func TestTruncateRevisionConflictTextPreservesUTF8(t *testing.T) {
	t.Parallel()
	got, truncated := truncateRevisionConflictText("one 🧭 two", 6)
	if !truncated || !strings.HasPrefix(got, "one") || !strings.Contains(got, "…") || !utf8.ValidString(got) {
		t.Fatalf("truncateRevisionConflictText = %q, %v; want valid truncated UTF-8", got, truncated)
	}
}
