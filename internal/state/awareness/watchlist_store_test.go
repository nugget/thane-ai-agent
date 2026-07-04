package awareness

import (
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	_ "modernc.org/sqlite"
)

func setupTestStore(t *testing.T) *WatchlistStore {
	t.Helper()
	db, err := sql.Open("sqlite-thane", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewWatchlistStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func globalEntityIDs(t *testing.T, store *WatchlistStore) []string {
	t.Helper()
	rows, err := store.ListOwner("")
	if err != nil {
		t.Fatalf("ListOwner(\"\"): %v", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.EntityID)
	}
	return ids
}

func TestStore_ListEmpty(t *testing.T) {
	store := setupTestStore(t)

	rows, err := store.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected empty list, got %v", rows)
	}
}

func TestStore_UpsertAndListPreservesInsertionOrder(t *testing.T) {
	store := setupTestStore(t)

	if err := store.Upsert("", looppkg.EntitySubscription{EntityID: "sensor.office_temperature"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Distinct AddedAt so ORDER BY added_at is deterministic.
	if err := store.Upsert("", looppkg.EntitySubscription{
		EntityID: "binary_sensor.front_door",
		AddedAt:  time.Now().UTC().Add(time.Second),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	ids := globalEntityIDs(t, store)
	if !slices.Equal(ids, []string{"sensor.office_temperature", "binary_sensor.front_door"}) {
		t.Errorf("ids = %v, want insertion order", ids)
	}
}

func TestStore_UpsertReplacesInsteadOfDuplicating(t *testing.T) {
	store := setupTestStore(t)

	if err := store.Upsert("", looppkg.EntitySubscription{EntityID: "sensor.temperature"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.Upsert("", looppkg.EntitySubscription{
		EntityID: "sensor.temperature",
		History:  []int{600},
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	rows, err := store.ListOwner("")
	if err != nil {
		t.Fatalf("ListOwner: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after re-upsert, got %d", len(rows))
	}
	if !slices.Equal(rows[0].History, []int{600}) {
		t.Errorf("history = %v, want re-upserted options to win", rows[0].History)
	}
}

func TestStore_UpsertRoundTripsUnifiedOptions(t *testing.T) {
	store := setupTestStore(t)

	added := time.Now().UTC().Add(-time.Minute)
	if err := store.Upsert("kitchen-watcher", looppkg.EntitySubscription{
		EntityID:   "weather.home",
		History:    []int{600, 3600},
		Forecast:   "daily",
		TTLSeconds: 600,
		AddedAt:    added,
		Mode:       looppkg.SubscriptionModeBoth,
		SelfOnly:   true,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	rows, err := store.ListOwner("kitchen-watcher")
	if err != nil {
		t.Fatalf("ListOwner: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	row := rows[0]
	if row.Owner != "kitchen-watcher" {
		t.Errorf("owner = %q, want kitchen-watcher", row.Owner)
	}
	if !slices.Equal(row.History, []int{600, 3600}) {
		t.Errorf("history = %v, want [600 3600]", row.History)
	}
	if row.Forecast != "daily" {
		t.Errorf("forecast = %q, want daily", row.Forecast)
	}
	if row.TTLSeconds != 600 {
		t.Errorf("ttl = %d, want 600", row.TTLSeconds)
	}
	if row.Mode != looppkg.SubscriptionModeBoth {
		t.Errorf("mode = %q, want both", row.Mode)
	}
	if !row.SelfOnly {
		t.Error("self_only lost in round trip")
	}
	if row.AddedAt.IsZero() {
		t.Error("added_at not scanned from column")
	}
}

func TestStore_UpsertRejectsInvalidOptions(t *testing.T) {
	store := setupTestStore(t)

	if err := store.Upsert("", looppkg.EntitySubscription{EntityID: "weather.home", Forecast: "monthly"}); err == nil {
		t.Fatal("expected invalid forecast error")
	} else if !strings.Contains(err.Error(), "forecast must be one of") {
		t.Fatalf("error = %q, want forecast validation", err.Error())
	}

	if err := store.Upsert("", looppkg.EntitySubscription{EntityID: "sensor.a", Mode: "firehose"}); err == nil {
		t.Fatal("expected invalid mode error")
	}

	if err := store.Upsert("", looppkg.EntitySubscription{EntityID: ""}); err == nil {
		t.Fatal("expected entity_id required error")
	}
}

func TestStore_RemoveIsOwnerExact(t *testing.T) {
	store := setupTestStore(t)

	if err := store.Upsert("", looppkg.EntitySubscription{EntityID: "sensor.battery"}); err != nil {
		t.Fatalf("upsert global: %v", err)
	}
	if err := store.Upsert("battery-watcher", looppkg.EntitySubscription{EntityID: "sensor.battery"}); err != nil {
		t.Fatalf("upsert owned: %v", err)
	}

	if err := store.Remove("battery-watcher", "sensor.battery"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if ids := globalEntityIDs(t, store); !slices.Equal(ids, []string{"sensor.battery"}) {
		t.Errorf("global tier = %v, want untouched [sensor.battery]", ids)
	}
	owned, err := store.ListOwner("battery-watcher")
	if err != nil {
		t.Fatalf("ListOwner: %v", err)
	}
	if len(owned) != 0 {
		t.Errorf("owned rows = %v, want empty", owned)
	}

	// Removing a non-existent row is a no-op.
	if err := store.Remove("", "sensor.does_not_exist"); err != nil {
		t.Fatalf("remove non-existent: %v", err)
	}
}

func TestStore_ReplaceOwnerIsAtomicAndSkipsExpired(t *testing.T) {
	store := setupTestStore(t)

	if err := store.Upsert("loop-a", looppkg.EntitySubscription{EntityID: "sensor.old"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := store.ReplaceOwner("loop-a", []looppkg.EntitySubscription{
		{EntityID: "sensor.new_one"},
		{EntityID: "sensor.already_expired", TTLSeconds: 60, AddedAt: time.Now().UTC().Add(-time.Hour)},
		{EntityID: ""}, // silently skipped, not an error
	})
	if err != nil {
		t.Fatalf("ReplaceOwner: %v", err)
	}

	rows, err := store.ListOwner("loop-a")
	if err != nil {
		t.Fatalf("ListOwner: %v", err)
	}
	if len(rows) != 1 || rows[0].EntityID != "sensor.new_one" {
		t.Fatalf("rows = %+v, want exactly sensor.new_one", rows)
	}

	if err := store.ReplaceOwner("", nil); err == nil {
		t.Fatal("ReplaceOwner with empty owner must error — the global tier is never bulk-replaced")
	}
	if err := store.RemoveAllForOwner(""); err == nil {
		t.Fatal("RemoveAllForOwner with empty owner must error")
	}
}

func TestStore_OwnersExcludesGlobalTier(t *testing.T) {
	store := setupTestStore(t)

	if err := store.Upsert("", looppkg.EntitySubscription{EntityID: "sensor.global"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.Upsert("loop-b", looppkg.EntitySubscription{EntityID: "sensor.b"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.Upsert(OwnerSystem, looppkg.EntitySubscription{EntityID: "person.alice", Mode: looppkg.SubscriptionModeIngest}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	owners, err := store.Owners()
	if err != nil {
		t.Fatalf("Owners: %v", err)
	}
	if !slices.Equal(owners, []string{"loop-b", OwnerSystem}) {
		t.Errorf("owners = %v, want [loop-b system]", owners)
	}
}

func TestStore_ExpiredRowsAreReapedOnScan(t *testing.T) {
	store := setupTestStore(t)

	if err := store.Upsert("battery-watcher", looppkg.EntitySubscription{
		EntityID:   "sensor.expired_battery",
		TTLSeconds: 60,
		AddedAt:    time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	rows, err := store.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ListAll() = %v, want empty after cleanup", rows)
	}

	count, err := store.subscriptionCount()
	if err != nil {
		t.Fatalf("subscriptionCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("subscriptionCount() = %d, want 0 after cleanup", count)
	}
}

// TestStore_LegacyExpiresAtShim covers pre-#1209 rows whose options
// blob carried an absolute expires_at instead of ttl_seconds: a
// still-live expiry keeps counting down against the added_at column,
// and an elapsed one is reaped on the next scan.
func TestStore_LegacyExpiresAtShim(t *testing.T) {
	store := setupTestStore(t)

	insertLegacy := func(entityID, expiresAt string) {
		t.Helper()
		optsJSON, err := json.Marshal(map[string]any{"expires_at": expiresAt})
		if err != nil {
			t.Fatalf("marshal legacy options: %v", err)
		}
		if _, err := store.db.Exec(
			`INSERT INTO watched_entity_subscriptions (entity_id, owner, added_at, options) VALUES (?, '', ?, ?)`,
			entityID, time.Now().UTC().Add(-time.Minute), string(optsJSON),
		); err != nil {
			t.Fatalf("insert legacy row: %v", err)
		}
	}

	insertLegacy("sensor.legacy_live", time.Now().UTC().Add(time.Hour).Format(time.RFC3339))
	insertLegacy("sensor.legacy_elapsed", time.Now().UTC().Add(-time.Minute).Format(time.RFC3339))

	rows, err := store.ListOwner("")
	if err != nil {
		t.Fatalf("ListOwner: %v", err)
	}
	if len(rows) != 1 || rows[0].EntityID != "sensor.legacy_live" {
		t.Fatalf("rows = %+v, want only sensor.legacy_live to survive", rows)
	}
	// Recovered TTL ≈ expires_at − added_at ≈ 61 minutes; assert the
	// order of magnitude rather than an exact second to stay clock-safe.
	if rows[0].TTLSeconds < 3600 || rows[0].TTLSeconds > 3780 {
		t.Errorf("recovered ttl = %ds, want ~3660s", rows[0].TTLSeconds)
	}
}

// TestStore_MigratesLegacyScopeColumn proves the scope→owner column
// rename lands on a database created with the pre-#1209 schema and
// that its rows stay readable afterwards.
func TestStore_MigratesLegacyScopeColumn(t *testing.T) {
	db, err := sql.Open("sqlite-thane", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`CREATE TABLE watched_entity_subscriptions (
		entity_id TEXT NOT NULL,
		scope     TEXT NOT NULL DEFAULT '',
		added_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		options   TEXT NOT NULL DEFAULT '{}',
		PRIMARY KEY (scope, entity_id)
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO watched_entity_subscriptions (entity_id, scope, options) VALUES ('sensor.pre_rename', '', '{}')`,
	); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	store, err := NewWatchlistStore(db, nil)
	if err != nil {
		t.Fatalf("new store over legacy schema: %v", err)
	}

	if ids := globalEntityIDs(t, store); !slices.Equal(ids, []string{"sensor.pre_rename"}) {
		t.Errorf("post-migration ids = %v, want [sensor.pre_rename]", ids)
	}

	// A second migration pass must be a no-op.
	if _, err := NewWatchlistStore(db, nil); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
}

// TestStore_GlobalEntityGates covers the narrow dedup lookup used by
// [LoopSubscriptionProvider]: it must return only the candidates
// present in the always-visible (owner=”) tier — each with its
// RequiresTag gate so the caller can ignore gated-off rows — ignore
// owned rows for the same entity_id, and skip the TTL cleanup side
// effect that [ListOwner] performs.
func TestStore_GlobalEntityGates(t *testing.T) {
	store := setupTestStore(t)

	// Global ungated → present with an empty gate.
	if err := store.Upsert("", looppkg.EntitySubscription{EntityID: "sensor.always_on"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Global gated → present with its gate exposed.
	if err := store.Upsert("", looppkg.EntitySubscription{EntityID: "sensor.lensed", RequiresTag: "ranch_water"}); err != nil {
		t.Fatalf("upsert gated: %v", err)
	}
	// Loop-owned → NOT in the always-visible set even though the
	// entity_id matches a candidate.
	if err := store.Upsert("focus-loop", looppkg.EntitySubscription{EntityID: "sensor.owned_only"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	gates, err := store.GlobalEntityGates([]string{
		"sensor.always_on",
		"sensor.lensed",
		"sensor.owned_only",
		"sensor.unknown",
	})
	if err != nil {
		t.Fatalf("GlobalEntityGates: %v", err)
	}
	if gate, ok := gates["sensor.always_on"]; !ok || gate != "" {
		t.Errorf("sensor.always_on gate = %q,%v, want present and ungated", gate, ok)
	}
	if gate, ok := gates["sensor.lensed"]; !ok || gate != "ranch_water" {
		t.Errorf("sensor.lensed gate = %q,%v, want ranch_water", gate, ok)
	}
	if _, ok := gates["sensor.owned_only"]; ok {
		t.Errorf("sensor.owned_only leaked through despite loop ownership: %v", gates)
	}
	if _, ok := gates["sensor.unknown"]; ok {
		t.Errorf("sensor.unknown leaked through despite not being subscribed: %v", gates)
	}

	// Empty candidate list returns an empty (non-nil) map.
	empty, err := store.GlobalEntityGates(nil)
	if err != nil {
		t.Fatalf("GlobalEntityGates(nil): %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("want empty non-nil map, got %#v", empty)
	}

	// Duplicates and empty strings in the candidate list are
	// tolerated (deduped + filtered before the SQL IN clause).
	gates2, err := store.GlobalEntityGates([]string{"sensor.always_on", "sensor.always_on", ""})
	if err != nil {
		t.Fatalf("GlobalEntityGates dedup: %v", err)
	}
	if len(gates2) != 1 {
		t.Errorf("want exactly 1 entry after dedup, got %v", gates2)
	}
}
