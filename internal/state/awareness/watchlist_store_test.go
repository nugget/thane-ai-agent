package awareness

import (
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestStore(t *testing.T) *WatchlistStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewWatchlistStore(db)
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

func TestStore_AddWithOptionsScopedSubscriptions(t *testing.T) {
	store := setupTestStore(t)

	if err := store.AddWithOptions("sensor.battery", []string{"battery_focus", "interactive", "battery_focus"}, []int{600, 3600}, 300, "daily"); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	ids, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !slices.Equal(ids, []string{"sensor.battery"}) {
		t.Fatalf("List() = %v, want [sensor.battery]", ids)
	}

	untagged, err := store.ListUntagged()
	if err != nil {
		t.Fatalf("ListUntagged: %v", err)
	}
	if len(untagged) != 0 {
		t.Fatalf("ListUntagged() = %v, want empty", untagged)
	}

	tags, err := store.DistinctTags()
	if err != nil {
		t.Fatalf("DistinctTags: %v", err)
	}
	if !slices.Equal(tags, []string{"battery_focus", "interactive"}) {
		t.Fatalf("DistinctTags() = %v, want [battery_focus interactive]", tags)
	}

	batteryFocus, err := store.ListByTag("battery_focus")
	if err != nil {
		t.Fatalf("ListByTag(battery_focus): %v", err)
	}
	if len(batteryFocus) != 1 {
		t.Fatalf("ListByTag(battery_focus) len = %d, want 1", len(batteryFocus))
	}
	if batteryFocus[0].Scope != "battery_focus" {
		t.Fatalf("scope = %q, want battery_focus", batteryFocus[0].Scope)
	}
	if !slices.Equal(batteryFocus[0].History, []int{600, 3600}) {
		t.Fatalf("history = %v, want [600 3600]", batteryFocus[0].History)
	}
	if batteryFocus[0].Forecast != "daily" {
		t.Fatalf("forecast = %q, want daily", batteryFocus[0].Forecast)
	}
	if batteryFocus[0].ExpiresAt == nil {
		t.Fatal("expected expiration to be set")
	}
}

func TestStore_AddWithOptionsRejectsInvalidForecast(t *testing.T) {
	store := setupTestStore(t)

	err := store.AddWithOptions("weather.home", nil, nil, 0, "monthly")
	if err == nil {
		t.Fatal("expected invalid forecast error")
	}
	if !strings.Contains(err.Error(), "forecast must be one of") {
		t.Fatalf("error = %q, want forecast validation", err.Error())
	}
}

func TestStore_RemoveWithScopesPreservesOtherSubscriptions(t *testing.T) {
	store := setupTestStore(t)

	if err := store.Add("sensor.battery"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.AddWithOptions("sensor.battery", []string{"battery_focus"}, nil, 0, ""); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	if err := store.RemoveWithScopes("sensor.battery", []string{"battery_focus"}); err != nil {
		t.Fatalf("RemoveWithScopes: %v", err)
	}

	untagged, err := store.ListUntagged()
	if err != nil {
		t.Fatalf("ListUntagged: %v", err)
	}
	if !slices.Equal(untagged, []string{"sensor.battery"}) {
		t.Fatalf("ListUntagged() = %v, want [sensor.battery]", untagged)
	}

	scoped, err := store.ListByTag("battery_focus")
	if err != nil {
		t.Fatalf("ListByTag: %v", err)
	}
	if len(scoped) != 0 {
		t.Fatalf("ListByTag(battery_focus) = %v, want empty", scoped)
	}
}

func TestStore_ListSubscriptionsSkipsExpiredAndCleansUp(t *testing.T) {
	store := setupTestStore(t)

	wire := watchlistOptionsWire{
		ExpiresAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	}
	optsJSON, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal options: %v", err)
	}

	if _, err := store.db.Exec(
		`INSERT INTO watched_entity_subscriptions (entity_id, scope, options) VALUES (?, ?, ?)`,
		"sensor.expired_battery", "battery_focus", string(optsJSON),
	); err != nil {
		t.Fatalf("insert expired subscription: %v", err)
	}

	subs, err := store.ListSubscriptions("")
	if err != nil {
		t.Fatalf("ListSubscriptions: %v", err)
	}
	if len(subs) != 0 {
		t.Fatalf("ListSubscriptions() = %v, want empty after cleanup", subs)
	}

	count, err := store.subscriptionCount()
	if err != nil {
		t.Fatalf("subscriptionCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("subscriptionCount() = %d, want 0 after cleanup", count)
	}
}
