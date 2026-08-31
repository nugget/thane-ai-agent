package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

type revisionMutationBackend struct {
	root     string
	revision string
	next     int
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
		return "", fmt.Errorf("revision conflict: expected %s, current is %s", expectedRevision, actual)
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

func (b *revisionMutationBackend) Diff(context.Context, string, string, string, string) (RevisionDiff, error) {
	return RevisionDiff{}, nil
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
	return nil
}

func TestRevisionCheckedMutationRoundTripAndConflict(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := &revisionMutationBackend{root: root}
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
		Ref:              "contacts:person.md",
		Body:             stringPtr("First fact."),
		ExpectedRevision: "absent",
	})
	if err != nil {
		t.Fatalf("Write with absent revision: %v", err)
	}
	var created modelMutationResult
	if err := json.Unmarshal([]byte(createdJSON), &created); err != nil {
		t.Fatalf("unmarshal create result: %v", err)
	}
	if created.Revision != "rev-1" {
		t.Fatalf("create revision = %q, want rev-1", created.Revision)
	}

	readJSON, err := tools.Read(ctx, RefArgs{Ref: "contacts:person.md"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var read modelDocumentRecord
	if err := json.Unmarshal([]byte(readJSON), &read); err != nil {
		t.Fatalf("unmarshal read result: %v", err)
	}
	if read.Revision != created.Revision {
		t.Fatalf("read revision = %q, want %q", read.Revision, created.Revision)
	}

	updatedJSON, err := tools.Edit(ctx, EditArgs{
		Ref:              "contacts:person.md",
		Mode:             "append_body",
		Body:             "Second fact.",
		ExpectedRevision: created.Revision,
	})
	if err != nil {
		t.Fatalf("Edit with current revision: %v", err)
	}
	var updated modelMutationResult
	if err := json.Unmarshal([]byte(updatedJSON), &updated); err != nil {
		t.Fatalf("unmarshal edit result: %v", err)
	}
	if updated.Revision != "rev-2" {
		t.Fatalf("updated revision = %q, want rev-2", updated.Revision)
	}

	_, err = tools.Edit(ctx, EditArgs{
		Ref:              "contacts:person.md",
		Mode:             "append_body",
		Body:             "Stale fact.",
		ExpectedRevision: created.Revision,
	})
	if err == nil || !strings.Contains(err.Error(), "revision conflict") {
		t.Fatalf("stale Edit error = %v, want revision conflict", err)
	}
	record, err := store.Read(ctx, "contacts:person.md")
	if err != nil {
		t.Fatalf("Read after stale edit: %v", err)
	}
	if !strings.Contains(record.Body, "Second fact.") || strings.Contains(record.Body, "Stale fact.") {
		t.Fatalf("body after stale edit = %q, want second fact without stale fact", record.Body)
	}
}
