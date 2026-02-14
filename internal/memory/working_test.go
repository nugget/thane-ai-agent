package memory

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func newTestWorkingMemoryStore(t *testing.T) *WorkingMemoryStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s, err := NewWorkingMemoryStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

func TestWorkingMemory_GetEmpty(t *testing.T) {
	s := newTestWorkingMemoryStore(t)

	content, updatedAt, err := s.Get("default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty content, got %q", content)
	}
	if !updatedAt.IsZero() {
		t.Errorf("expected zero time, got %v", updatedAt)
	}
}

func TestWorkingMemory_SetAndGet(t *testing.T) {
	s := newTestWorkingMemoryStore(t)

	before := time.Now().UTC().Add(-time.Second)
	if err := s.Set("default", "feeling productive today"); err != nil {
		t.Fatalf("set: %v", err)
	}

	content, updatedAt, err := s.Get("default")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if content != "feeling productive today" {
		t.Errorf("got %q, want %q", content, "feeling productive today")
	}
	if updatedAt.Before(before) {
		t.Errorf("updated_at %v is before test start %v", updatedAt, before)
	}
}

func TestWorkingMemory_Upsert(t *testing.T) {
	s := newTestWorkingMemoryStore(t)

	if err := s.Set("default", "first version"); err != nil {
		t.Fatalf("first set: %v", err)
	}
	if err := s.Set("default", "second version"); err != nil {
		t.Fatalf("second set: %v", err)
	}

	content, _, err := s.Get("default")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if content != "second version" {
		t.Errorf("got %q, want %q", content, "second version")
	}
}

func TestWorkingMemory_MultiConversation(t *testing.T) {
	s := newTestWorkingMemoryStore(t)

	if err := s.Set("conv-a", "memory for A"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("conv-b", "memory for B"); err != nil {
		t.Fatal(err)
	}

	contentA, _, err := s.Get("conv-a")
	if err != nil {
		t.Fatal(err)
	}
	contentB, _, err := s.Get("conv-b")
	if err != nil {
		t.Fatal(err)
	}

	if contentA != "memory for A" {
		t.Errorf("conv-a: got %q", contentA)
	}
	if contentB != "memory for B" {
		t.Errorf("conv-b: got %q", contentB)
	}
}

func TestWorkingMemory_Delete(t *testing.T) {
	s := newTestWorkingMemoryStore(t)

	if err := s.Set("default", "some content"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("default"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	content, _, err := s.Get("default")
	if err != nil {
		t.Fatal(err)
	}
	if content != "" {
		t.Errorf("expected empty after delete, got %q", content)
	}
}

func TestWorkingMemory_DeleteNonexistent(t *testing.T) {
	s := newTestWorkingMemoryStore(t)

	// Should not error when deleting something that doesn't exist.
	if err := s.Delete("nonexistent"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
