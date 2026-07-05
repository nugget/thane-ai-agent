package awareness

import (
	"database/sql"
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
	rows, err := store.ListOwner(OwnerCore)
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

	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.office_temperature"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Distinct AddedAt so ORDER BY added_at is deterministic.
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{
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

	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.temperature"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{
		EntityID: "sensor.temperature",
		History:  []int{600},
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	rows, err := store.ListOwner(OwnerCore)
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

	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "weather.home", Forecast: "monthly"}); err == nil {
		t.Fatal("expected invalid forecast error")
	} else if !strings.Contains(err.Error(), "forecast must be one of") {
		t.Fatalf("error = %q, want forecast validation", err.Error())
	}

	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.a", Mode: "firehose"}); err == nil {
		t.Fatal("expected invalid mode error")
	}

	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: ""}); err == nil {
		t.Fatal("expected entity_id required error")
	}
}

func TestStore_RemoveIsOwnerExact(t *testing.T) {
	store := setupTestStore(t)

	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.battery"}); err != nil {
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

func TestStore_OwnersListsReservedAndLoopOwners(t *testing.T) {
	store := setupTestStore(t)

	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.global"}); err != nil {
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
	// Core appears like any other owner post-#1208; the orphan sweep
	// is what treats it (and system) as reserved.
	if !slices.Equal(owners, []string{OwnerCore, "loop-b", OwnerSystem}) {
		t.Errorf("owners = %v, want [core loop-b system]", owners)
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
	// Seed under a NAMED owner: anonymous-tier rows are deliberately
	// cleared (not migrated) by the #1208 closing step, so the rename
	// mechanics are proven on a row that survives.
	if _, err := db.Exec(
		`INSERT INTO watched_entity_subscriptions (entity_id, scope, options) VALUES ('sensor.pre_rename', 'kitchen-watcher', '{}')`,
	); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	store, err := NewWatchlistStore(db, nil)
	if err != nil {
		t.Fatalf("new store over legacy schema: %v", err)
	}

	rows, err := store.ListOwner("kitchen-watcher")
	if err != nil {
		t.Fatalf("ListOwner: %v", err)
	}
	if len(rows) != 1 || rows[0].EntityID != "sensor.pre_rename" {
		t.Errorf("post-migration rows = %+v, want [sensor.pre_rename] under the renamed owner column", rows)
	}

	// A second migration pass must be a no-op.
	if _, err := NewWatchlistStore(db, nil); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
}

// TestStore_CoreEntityGates covers the narrow dedup lookup used by
// [LoopSubscriptionProvider]: it must return only the candidates
// present in the always-visible (owner=”) tier — each with its
// RequiresTag gate so the caller can ignore gated-off rows — ignore
// owned rows for the same entity_id, and skip the TTL cleanup side
// effect that [ListOwner] performs.
func TestStore_CoreEntityGates(t *testing.T) {
	store := setupTestStore(t)

	// Global ungated → present with an empty gate.
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.always_on"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Global gated → present with its gate exposed.
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.lensed", RequiresTag: "ranch_water"}); err != nil {
		t.Fatalf("upsert gated: %v", err)
	}
	// Loop-owned → NOT in the always-visible set even though the
	// entity_id matches a candidate.
	if err := store.Upsert("focus-loop", looppkg.EntitySubscription{EntityID: "sensor.owned_only"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Rows that never render must not appear at all: ingest-only mode
	// and elapsed TTL (Copilot #1214).
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.capture_only", Mode: looppkg.SubscriptionModeIngest}); err != nil {
		t.Fatalf("upsert ingest-only: %v", err)
	}
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{
		EntityID:   "sensor.elapsed",
		TTLSeconds: 60,
		AddedAt:    time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("upsert elapsed: %v", err)
	}

	gates, err := store.CoreEntityGates([]string{
		"sensor.always_on",
		"sensor.lensed",
		"sensor.owned_only",
		"sensor.capture_only",
		"sensor.elapsed",
		"sensor.unknown",
	})
	if err != nil {
		t.Fatalf("CoreEntityGates: %v", err)
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
	if _, ok := gates["sensor.capture_only"]; ok {
		t.Errorf("ingest-only row leaked into the dedup set: %v", gates)
	}
	if _, ok := gates["sensor.elapsed"]; ok {
		t.Errorf("expired row leaked into the dedup set: %v", gates)
	}
	if _, ok := gates["sensor.unknown"]; ok {
		t.Errorf("sensor.unknown leaked through despite not being subscribed: %v", gates)
	}

	// Empty candidate list returns an empty (non-nil) map.
	empty, err := store.CoreEntityGates(nil)
	if err != nil {
		t.Fatalf("CoreEntityGates(nil): %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("want empty non-nil map, got %#v", empty)
	}

	// Duplicates and empty strings in the candidate list are
	// tolerated (deduped + filtered before the SQL IN clause).
	gates2, err := store.CoreEntityGates([]string{"sensor.always_on", "sensor.always_on", ""})
	if err != nil {
		t.Fatalf("CoreEntityGates dedup: %v", err)
	}
	if len(gates2) != 1 {
		t.Errorf("want exactly 1 entry after dedup, got %v", gates2)
	}
}

// TestStore_ClearsRetiredAnonymousTier proves the #1208 closing
// posture: legacy empty-owner rows are deliberately NOT migrated onto
// core — the owner re-evaluates the subscription set by hand — so the
// schema migration clears them, leaves genuine core rows untouched,
// and is a no-op on every subsequent open.
func TestStore_ClearsRetiredAnonymousTier(t *testing.T) {
	db, err := sql.Open("sqlite-thane", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Pre-#1208 shape: anonymous-tier rows alongside a core-owned row.
	if _, err := db.Exec(`CREATE TABLE watched_entity_subscriptions (
		entity_id TEXT NOT NULL,
		owner     TEXT NOT NULL DEFAULT '',
		added_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		options   TEXT NOT NULL DEFAULT '{}',
		PRIMARY KEY (owner, entity_id)
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	for _, row := range [][2]string{
		{"sensor.legacy_one", ""},
		{"sensor.legacy_two", ""},
		{"sensor.kept", "core"},
	} {
		if _, err := db.Exec(
			`INSERT INTO watched_entity_subscriptions (entity_id, owner) VALUES (?, ?)`,
			row[0], row[1],
		); err != nil {
			t.Fatalf("seed %s: %v", row[0], err)
		}
	}

	store, err := NewWatchlistStore(db, nil)
	if err != nil {
		t.Fatalf("new store over legacy tier: %v", err)
	}

	rows, err := store.ListOwner(OwnerCore)
	if err != nil {
		t.Fatalf("ListOwner(core): %v", err)
	}
	if len(rows) != 1 || rows[0].EntityID != "sensor.kept" {
		t.Fatalf("core rows = %+v, want only the pre-existing core row", rows)
	}
	var leftover int
	if err := db.QueryRow(`SELECT COUNT(*) FROM watched_entity_subscriptions WHERE owner = ''`).Scan(&leftover); err != nil {
		t.Fatalf("count leftovers: %v", err)
	}
	if leftover != 0 {
		t.Errorf("leftover anonymous rows = %d, want cleared", leftover)
	}
	if _, err := NewWatchlistStore(db, nil); err != nil {
		t.Fatalf("re-open: %v", err)
	}
}
