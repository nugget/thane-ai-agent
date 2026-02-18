package watchlist

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func setupTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func TestStore_ListEmpty(t *testing.T) {
	store := setupTestStore(t)

	ids, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected empty list, got %v", ids)
	}
}

func TestStore_AddAndList(t *testing.T) {
	store := setupTestStore(t)

	if err := store.Add("sensor.office_temperature"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := store.Add("binary_sensor.front_door"); err != nil {
		t.Fatalf("add: %v", err)
	}

	ids, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(ids))
	}
	if ids[0] != "sensor.office_temperature" {
		t.Errorf("ids[0] = %q, want %q", ids[0], "sensor.office_temperature")
	}
	if ids[1] != "binary_sensor.front_door" {
		t.Errorf("ids[1] = %q, want %q", ids[1], "binary_sensor.front_door")
	}
}

func TestStore_AddDuplicate(t *testing.T) {
	store := setupTestStore(t)

	if err := store.Add("sensor.temperature"); err != nil {
		t.Fatalf("add: %v", err)
	}
	// Second add of the same entity should be a no-op.
	if err := store.Add("sensor.temperature"); err != nil {
		t.Fatalf("add duplicate: %v", err)
	}

	ids, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("expected 1 entity after duplicate add, got %d", len(ids))
	}
}

func TestStore_Remove(t *testing.T) {
	store := setupTestStore(t)

	if err := store.Add("sensor.temperature"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := store.Remove("sensor.temperature"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	ids, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected empty list after remove, got %v", ids)
	}
}

func TestStore_RemoveNonExistent(t *testing.T) {
	store := setupTestStore(t)

	// Removing a non-existent entity should be a no-op.
	if err := store.Remove("sensor.does_not_exist"); err != nil {
		t.Fatalf("remove non-existent: %v", err)
	}
}
