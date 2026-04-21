package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/awareness"
	_ "modernc.org/sqlite"
)

func setupWatchlistRegistry(t *testing.T) (*Registry, *awareness.WatchlistStore) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := awareness.NewWatchlistStore(db)
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
	if r.Get("list_context_entities") == nil {
		t.Error("list_context_entities should be registered")
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

func TestAddContextEntity_WithScopesTTLAndHistory(t *testing.T) {
	r, store := setupWatchlistRegistry(t)

	var registered []string
	r.OnWatchlistTagAdded(func(tag string) {
		registered = append(registered, tag)
	})

	result, err := r.handleAddContextEntity(context.Background(), map[string]any{
		"entity_id":   "sensor.battery",
		"tags":        []any{"battery_focus", "battery_focus"},
		"history":     []any{60, 3600},
		"ttl_seconds": 120,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "expires in 120s") {
		t.Fatalf("result = %q, want TTL text", result)
	}

	if !slices.Equal(registered, []string{"battery_focus"}) {
		t.Fatalf("registered tags = %v, want [battery_focus]", registered)
	}

	subs, err := store.ListByTag("battery_focus")
	if err != nil {
		t.Fatalf("ListByTag: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("ListByTag len = %d, want 1", len(subs))
	}
	if !slices.Equal(subs[0].History, []int{60, 3600}) {
		t.Fatalf("history = %v, want [60 3600]", subs[0].History)
	}
	if subs[0].ExpiresAt == nil {
		t.Fatal("expected subscription expiration")
	}
}

func TestParseTagArgs_IgnoresWhitespaceOnlyTags(t *testing.T) {
	tags, err := parseTagArgs([]any{"battery_focus", "   ", "\t", " interactive "})
	if err != nil {
		t.Fatalf("parseTagArgs: %v", err)
	}

	if !slices.Equal(tags, []string{"battery_focus", "interactive"}) {
		t.Fatalf("parseTagArgs() = %v, want [battery_focus interactive]", tags)
	}
}

func TestListContextEntities_ReturnsScopedSubscriptions(t *testing.T) {
	r, store := setupWatchlistRegistry(t)

	if err := store.Add("sensor.always_on"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.AddWithOptions("sensor.battery", []string{"battery_focus"}, []int{600}, 300); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	raw, err := r.handleListContextEntities(context.Background(), map[string]any{
		"tag": "battery_focus",
	})
	if err != nil {
		t.Fatalf("handleListContextEntities: %v", err)
	}

	var payload struct {
		Count int `json:"count"`
		Items []struct {
			EntityID      string `json:"entity_id"`
			Scope         string `json:"scope"`
			AlwaysVisible bool   `json:"always_visible"`
			ExpiresAt     string `json:"expires_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Count != 1 {
		t.Fatalf("count = %d, want 1", payload.Count)
	}
	if payload.Items[0].EntityID != "sensor.battery" || payload.Items[0].Scope != "battery_focus" {
		t.Fatalf("item = %+v, want sensor.battery/battery_focus", payload.Items[0])
	}
	if payload.Items[0].AlwaysVisible {
		t.Fatal("tagged subscription should not be always visible")
	}
	if payload.Items[0].ExpiresAt == "" {
		t.Fatal("expected expires_at in payload")
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

func TestRemoveContextEntity_ScopedRemovalKeepsOtherSubscriptions(t *testing.T) {
	r, store := setupWatchlistRegistry(t)

	if err := store.Add("sensor.battery"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.AddWithOptions("sensor.battery", []string{"battery_focus"}, nil, 0); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	result, err := r.handleRemoveContextEntity(context.Background(), map[string]any{
		"entity_id": "sensor.battery",
		"tags":      []any{"battery_focus"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "scopes [battery_focus]") {
		t.Fatalf("result = %q, want scoped-removal text", result)
	}

	untagged, err := store.ListUntagged()
	if err != nil {
		t.Fatalf("ListUntagged: %v", err)
	}
	if !slices.Equal(untagged, []string{"sensor.battery"}) {
		t.Fatalf("ListUntagged() = %v, want [sensor.battery]", untagged)
	}

	tagged, err := store.ListByTag("battery_focus")
	if err != nil {
		t.Fatalf("ListByTag: %v", err)
	}
	if len(tagged) != 0 {
		t.Fatalf("ListByTag(battery_focus) = %v, want empty", tagged)
	}
}
