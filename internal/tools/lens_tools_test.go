package tools

import (
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/opstate"
)

func newTestLensStore(t *testing.T) *LensStore {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := opstate.NewStore(db)
	if err != nil {
		t.Fatalf("create opstate store: %v", err)
	}
	return NewLensStore(store)
}

func TestLensStore_AddAndList(t *testing.T) {
	s := newTestLensStore(t)

	if err := s.Add("night_quiet"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	lenses, err := s.ActiveLenses()
	if err != nil {
		t.Fatalf("ActiveLenses: %v", err)
	}
	if len(lenses) != 1 || lenses[0] != "night_quiet" {
		t.Fatalf("expected [night_quiet], got %v", lenses)
	}
}

func TestLensStore_AddDuplicate(t *testing.T) {
	s := newTestLensStore(t)

	if err := s.Add("storm_watch"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Add("storm_watch"); err != nil {
		t.Fatalf("Add duplicate: %v", err)
	}

	lenses, err := s.ActiveLenses()
	if err != nil {
		t.Fatalf("ActiveLenses: %v", err)
	}
	if len(lenses) != 1 {
		t.Fatalf("expected 1 lens, got %d: %v", len(lenses), lenses)
	}
}

func TestLensStore_Remove(t *testing.T) {
	s := newTestLensStore(t)

	if err := s.Add("night_quiet"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Remove("night_quiet"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	lenses, err := s.ActiveLenses()
	if err != nil {
		t.Fatalf("ActiveLenses: %v", err)
	}
	if len(lenses) != 0 {
		t.Fatalf("expected empty, got %v", lenses)
	}
}

func TestLensStore_RemoveNonexistent(t *testing.T) {
	s := newTestLensStore(t)

	if err := s.Remove("does_not_exist"); err != nil {
		t.Fatalf("Remove nonexistent: %v", err)
	}
}

func TestLensStore_EmptyList(t *testing.T) {
	s := newTestLensStore(t)

	lenses, err := s.ActiveLenses()
	if err != nil {
		t.Fatalf("ActiveLenses: %v", err)
	}
	if lenses != nil {
		t.Fatalf("expected nil, got %v", lenses)
	}
}

func TestLensStore_Persistence(t *testing.T) {
	// Use a shared DB connection — data written through one LensStore
	// is visible through another backed by the same connection.
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()

	store1, err := opstate.NewStore(db)
	if err != nil {
		t.Fatalf("create store1: %v", err)
	}
	ls1 := NewLensStore(store1)

	if err := ls1.Add("everyone_away"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Create a new LensStore on the same database to verify persistence.
	store2, err := opstate.NewStore(db)
	if err != nil {
		t.Fatalf("create store2: %v", err)
	}
	ls2 := NewLensStore(store2)

	lenses, err := ls2.ActiveLenses()
	if err != nil {
		t.Fatalf("ActiveLenses: %v", err)
	}
	if len(lenses) != 1 || lenses[0] != "everyone_away" {
		t.Fatalf("expected [everyone_away], got %v", lenses)
	}
}
