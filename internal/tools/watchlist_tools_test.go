package tools

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/watchlist"
	_ "modernc.org/sqlite"
)

func setupWatchlistRegistry(t *testing.T) (*Registry, *watchlist.Store) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := watchlist.NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	r := NewEmptyRegistry()
	r.SetWatchlistStore(store)
	return r, store
}

func TestSetWatchlistStore_RegistersTools(t *testing.T) {
	r, _ := setupWatchlistRegistry(t)

	if r.Get("add_context_entity") == nil {
		t.Error("add_context_entity should be registered")
	}
	if r.Get("remove_context_entity") == nil {
		t.Error("remove_context_entity should be registered")
	}
}

func TestAddContextEntity_MissingEntityID(t *testing.T) {
	r, _ := setupWatchlistRegistry(t)

	_, err := r.handleAddContextEntity(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing entity_id")
	}
	if !strings.Contains(err.Error(), "entity_id is required") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "entity_id is required")
	}
}

func TestAddContextEntity_Success(t *testing.T) {
	r, store := setupWatchlistRegistry(t)

	result, err := r.handleAddContextEntity(context.Background(), map[string]any{
		"entity_id": "sensor.temperature",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "sensor.temperature") {
		t.Errorf("result = %q, want to contain entity_id", result)
	}
	if !strings.Contains(result, "watching") {
		t.Errorf("result = %q, want to contain 'watching'", result)
	}

	// Verify it was actually stored.
	ids, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 1 || ids[0] != "sensor.temperature" {
		t.Errorf("store.List() = %v, want [sensor.temperature]", ids)
	}
}

func TestRemoveContextEntity_MissingEntityID(t *testing.T) {
	r, _ := setupWatchlistRegistry(t)

	_, err := r.handleRemoveContextEntity(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing entity_id")
	}
	if !strings.Contains(err.Error(), "entity_id is required") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "entity_id is required")
	}
}

func TestRemoveContextEntity_Success(t *testing.T) {
	r, store := setupWatchlistRegistry(t)

	// Add first, then remove.
	if err := store.Add("sensor.temperature"); err != nil {
		t.Fatalf("add: %v", err)
	}

	result, err := r.handleRemoveContextEntity(context.Background(), map[string]any{
		"entity_id": "sensor.temperature",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "sensor.temperature") {
		t.Errorf("result = %q, want to contain entity_id", result)
	}

	// Verify it was actually removed.
	ids, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("store.List() = %v, want empty", ids)
	}
}
