package memory

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func newTestWorkingMemoryProvider(t *testing.T, convID string) (*WorkingMemoryProvider, *WorkingMemoryStore) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewWorkingMemoryStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	convFunc := func(context.Context) string { return convID }
	return NewWorkingMemoryProvider(store, convFunc), store
}

func TestWorkingMemoryProvider_Empty(t *testing.T) {
	p, _ := newTestWorkingMemoryProvider(t, "default")

	got, err := p.GetContext(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestWorkingMemoryProvider_WithContent(t *testing.T) {
	p, store := newTestWorkingMemoryProvider(t, "default")

	if err := store.Set("default", "Feeling productive. Working on episodic memory."); err != nil {
		t.Fatal(err)
	}

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(got, "### Working Memory") {
		t.Error("expected Working Memory heading")
	}
	if !strings.Contains(got, "Feeling productive") {
		t.Error("expected working memory content")
	}
	if !strings.Contains(got, "Last updated") {
		t.Error("expected Last updated timestamp")
	}
}

func TestWorkingMemoryProvider_ConversationScoped(t *testing.T) {
	// Create a provider scoped to conv-b — should not see conv-a's memory.
	pB, store := newTestWorkingMemoryProvider(t, "conv-b")

	if err := store.Set("conv-a", "Memory for conversation A"); err != nil {
		t.Fatal(err)
	}

	got, err := pB.GetContext(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty for conv-b, got %q", got)
	}

	// Create a provider scoped to conv-a — should see its memory.
	convFuncA := func(context.Context) string { return "conv-a" }
	pA := NewWorkingMemoryProvider(store, convFuncA)

	got, err = pA.GetContext(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Memory for conversation A") {
		t.Error("expected conv-a working memory content")
	}
}
