package awareness

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// fakeLoopMutator records owner-addressed mutations and applies them
// to an in-memory per-loop subscription list, standing in for the
// app-side spec mutator.
type fakeLoopMutator struct {
	subs  map[string][]looppkg.EntitySubscription
	calls []string
}

func (f *fakeLoopMutator) mutate(_ context.Context, loopName string, mutate func([]looppkg.EntitySubscription) ([]looppkg.EntitySubscription, error)) ([]looppkg.EntitySubscription, error) {
	if f.subs == nil {
		f.subs = make(map[string][]looppkg.EntitySubscription)
	}
	f.calls = append(f.calls, loopName)
	next, err := mutate(f.subs[loopName])
	if err != nil {
		return nil, err
	}
	f.subs[loopName] = next
	return next, nil
}

func setupWatchlistProvider(t *testing.T) (*WatchlistTools, *WatchlistStore, *fakeLoopMutator) {
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

	mutator := &fakeLoopMutator{}
	p := NewWatchlistTools(WatchlistToolsConfig{
		Store:       store,
		LoopMutator: mutator.mutate,
	})
	return p, store, mutator
}

func TestWatchlistTools_NameAndToolList(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	if got := p.Name(); got != "awareness.watchlist" {
		t.Errorf("Name() = %q, want awareness.watchlist", got)
	}

	got := p.Tools()
	if len(got) != 3 {
		t.Fatalf("Tools() returned %d tools, want 3", len(got))
	}

	names := make([]string, 0, len(got))
	for _, tool := range got {
		names = append(names, tool.Name)
		if tool.Handler == nil {
			t.Errorf("tool %q has nil handler; provider contract requires non-nil", tool.Name)
		}
	}
	want := []string{"add_entity_subscription", "list_entity_subscriptions", "remove_entity_subscription"}
	slices.Sort(names)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("tool names = %v, want %v", names, want)
	}
}

func TestWatchlistTools_RegisterProviderAddsThreeTools(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	reg := tools.NewEmptyRegistry()
	reg.RegisterProvider(p)

	for _, name := range []string{"add_entity_subscription", "list_entity_subscriptions", "remove_entity_subscription"} {
		if reg.Get(name) == nil {
			t.Errorf("%s should be registered", name)
		}
	}
}

func TestAddEntitySubscription_MissingEntityID(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	_, err := p.handleAddEntitySubscription(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing entity_id")
	}
	if !strings.Contains(err.Error(), "entity_id is required") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "entity_id is required")
	}
}

func TestAddEntitySubscription_Success(t *testing.T) {
	p, store, _ := setupWatchlistProvider(t)

	result, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
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

	rows, err := store.ListOwner("")
	if err != nil {
		t.Fatalf("ListOwner: %v", err)
	}
	if len(rows) != 1 || rows[0].EntityID != "sensor.temperature" {
		t.Errorf("global rows = %+v, want [sensor.temperature]", rows)
	}
}

func TestAddEntitySubscription_WithTTLHistoryAndInclude(t *testing.T) {
	p, store, _ := setupWatchlistProvider(t)

	result, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id":   "weather.home",
		"history":     []any{60, 3600},
		"forecast":    "hourly",
		"ttl_seconds": 120,
		"include": map[string]any{
			"area":   true,
			"device": true,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "expires in 120s") {
		t.Fatalf("result = %q, want TTL text", result)
	}
	if !strings.Contains(result, "includes HA metadata") {
		t.Fatalf("result = %q, want include text", result)
	}

	rows, err := store.ListOwner("")
	if err != nil {
		t.Fatalf("ListOwner: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	row := rows[0]
	if !slices.Equal(row.History, []int{60, 3600}) {
		t.Fatalf("history = %v, want [60 3600]", row.History)
	}
	if row.Forecast != "hourly" {
		t.Fatalf("forecast = %q, want hourly", row.Forecast)
	}
	if row.TTLSeconds != 120 {
		t.Fatalf("ttl = %d, want 120", row.TTLSeconds)
	}
	if row.Include == nil || !row.Include.Area || !row.Include.Device {
		t.Fatalf("include = %#v, want area+device", row.Include)
	}
}

// TestAddEntitySubscription_RetiredTagsParam covers the #1209 teaching
// error: the lens-tag vocabulary is gone and the error must point the
// model at the owner parameter instead of silently ignoring tags.
func TestAddEntitySubscription_RetiredTagsParam(t *testing.T) {
	p, store, _ := setupWatchlistProvider(t)

	_, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "sensor.battery",
		"tags":      []any{"battery_focus"},
	})
	if err == nil {
		t.Fatal("expected teaching error for retired tags parameter")
	}
	if !strings.Contains(err.Error(), "retired") || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("error = %q, want retirement + owner guidance", err.Error())
	}

	count, err := store.subscriptionCount()
	if err != nil {
		t.Fatalf("subscriptionCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("row count = %d, want 0 — a rejected call must not write", count)
	}
}

func TestAddEntitySubscription_OwnerRoutesToLoopMutator(t *testing.T) {
	p, store, mutator := setupWatchlistProvider(t)

	result, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "sensor.kitchen_temp",
		"owner":     "kitchen-watcher",
		"self_only": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, `Loop "kitchen-watcher"`) {
		t.Fatalf("result = %q, want loop-owner phrasing", result)
	}
	if !slices.Equal(mutator.calls, []string{"kitchen-watcher"}) {
		t.Fatalf("mutator calls = %v, want [kitchen-watcher]", mutator.calls)
	}
	subs := mutator.subs["kitchen-watcher"]
	if len(subs) != 1 || subs[0].EntityID != "sensor.kitchen_temp" {
		t.Fatalf("loop subs = %+v, want sensor.kitchen_temp", subs)
	}
	if !subs[0].SelfOnly {
		t.Fatal("self_only not carried onto the loop subscription")
	}
	if subs[0].AddedAt.IsZero() {
		t.Fatal("AddedAt not stamped on the loop subscription")
	}

	// The owner path must not write a global row; the registry mirror
	// is the app-side persist hook's job.
	count, err := store.subscriptionCount()
	if err != nil {
		t.Fatalf("subscriptionCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("store row count = %d, want 0 for owner-routed add", count)
	}
}

func TestAddEntitySubscription_SelfOnlyRequiresOwner(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	_, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "sensor.kitchen_temp",
		"self_only": true,
	})
	if err == nil {
		t.Fatal("expected error for self_only without owner")
	}
	if !strings.Contains(err.Error(), "owner") {
		t.Fatalf("error = %q, want owner guidance", err.Error())
	}
}

func TestAddEntitySubscription_SystemOwnerRefused(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	_, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "person.alice",
		"owner":     OwnerSystem,
	})
	if err == nil {
		t.Fatal("expected reserved-owner error")
	}
	if !strings.Contains(err.Error(), "person.track") {
		t.Fatalf("error = %q, want config pointer", err.Error())
	}
}

func TestAddEntitySubscription_IngestModeFiresRebuildHook(t *testing.T) {
	db, err := sql.Open("sqlite-thane", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewWatchlistStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	rebuilds := 0
	p := NewWatchlistTools(WatchlistToolsConfig{
		Store:          store,
		OnIngestChange: func() { rebuilds++ },
	})

	if _, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "binary_sensor.*door*",
		"mode":      "ingest",
	}); err != nil {
		t.Fatalf("ingest add: %v", err)
	}
	if rebuilds != 1 {
		t.Fatalf("rebuilds = %d, want 1 after ingest add", rebuilds)
	}

	globs, err := store.IngestGlobs(time.Now())
	if err != nil {
		t.Fatalf("IngestGlobs: %v", err)
	}
	if !slices.Equal(globs, []string{"binary_sensor.*door*"}) {
		t.Fatalf("IngestGlobs = %v, want the new glob", globs)
	}

	// A render-mode add leaves the filter alone.
	if _, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "sensor.plain",
	}); err != nil {
		t.Fatalf("render add: %v", err)
	}
	if rebuilds != 1 {
		t.Fatalf("rebuilds = %d, want no rebuild for render add", rebuilds)
	}
}

func TestAddEntitySubscription_IngestRejectsRegistryTargets(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	_, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "area:office",
		"mode":      "ingest",
	})
	if err == nil {
		t.Fatal("expected registry-target rejection for ingest mode")
	}
	if !strings.Contains(err.Error(), "ingestion filter") {
		t.Fatalf("error = %q, want ingestion-filter guidance", err.Error())
	}
}

func TestAddEntitySubscription_InvalidForecast(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	_, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "weather.home",
		"forecast":  "monthly",
	})
	if err == nil {
		t.Fatal("expected error for invalid forecast")
	}
	if !strings.Contains(err.Error(), "forecast must be one of") {
		t.Fatalf("error = %q, want forecast validation", err.Error())
	}
}

func TestAddEntitySubscription_ForecastRequiresWeatherEntity(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	_, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "sensor.outdoor_temperature",
		"forecast":  "daily",
	})
	if err == nil {
		t.Fatal("expected error for forecast on non-weather entity")
	}
	if !strings.Contains(err.Error(), "weather.*") {
		t.Fatalf("error = %q, want weather entity guidance", err.Error())
	}
}

func TestAddEntitySubscription_ForecastNoneClearsExistingOption(t *testing.T) {
	p, store, _ := setupWatchlistProvider(t)

	if _, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "weather.home",
		"forecast":  "daily",
	}); err != nil {
		t.Fatalf("add forecast: %v", err)
	}
	result, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "weather.home",
		"forecast":  "none",
	})
	if err != nil {
		t.Fatalf("clear forecast: %v", err)
	}
	if !strings.Contains(result, "forecast: none") {
		t.Fatalf("result = %q, want forecast clearing note", result)
	}

	rows, err := store.ListOwner("")
	if err != nil {
		t.Fatalf("ListOwner: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].Forecast != "" {
		t.Fatalf("forecast = %q, want cleared", rows[0].Forecast)
	}
}

// TestListEntitySubscriptions_WholeTruth asserts the registry-wide
// listing: global, loop-owned, and system rows all appear with their
// owners, and the retired tag filter teaches instead of filtering.
func TestListEntitySubscriptions_WholeTruth(t *testing.T) {
	p, store, _ := setupWatchlistProvider(t)

	if err := store.Upsert("", looppkg.EntitySubscription{EntityID: "sensor.always_on"}); err != nil {
		t.Fatalf("upsert global: %v", err)
	}
	if err := store.Upsert("battery-watcher", looppkg.EntitySubscription{
		EntityID:   "sensor.battery",
		History:    []int{600},
		TTLSeconds: 300,
		AddedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("upsert owned: %v", err)
	}
	if err := store.Upsert(OwnerSystem, looppkg.EntitySubscription{
		EntityID: "person.alice",
		Mode:     looppkg.SubscriptionModeIngest,
	}); err != nil {
		t.Fatalf("upsert system: %v", err)
	}

	raw, err := p.handleListEntitySubscriptions(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("handleListEntitySubscriptions: %v", err)
	}

	var payload struct {
		Count int `json:"count"`
		Items []struct {
			EntityID      string `json:"entity_id"`
			Owner         string `json:"owner"`
			Mode          string `json:"mode"`
			AlwaysVisible bool   `json:"always_visible"`
			ExpiresDelta  string `json:"expires_delta"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Count != 3 {
		t.Fatalf("count = %d, want 3 (whole truth)", payload.Count)
	}
	byEntity := make(map[string]struct {
		Owner         string
		Mode          string
		AlwaysVisible bool
		ExpiresDelta  string
	}, len(payload.Items))
	for _, item := range payload.Items {
		byEntity[item.EntityID] = struct {
			Owner         string
			Mode          string
			AlwaysVisible bool
			ExpiresDelta  string
		}{item.Owner, item.Mode, item.AlwaysVisible, item.ExpiresDelta}
	}
	if got := byEntity["sensor.always_on"]; got.Owner != "" || !got.AlwaysVisible || got.Mode != "render" {
		t.Errorf("sensor.always_on = %+v, want global render row", got)
	}
	if got := byEntity["sensor.battery"]; got.Owner != "battery-watcher" || got.AlwaysVisible || got.ExpiresDelta == "" {
		t.Errorf("sensor.battery = %+v, want owned row with expiry", got)
	}
	if got := byEntity["person.alice"]; got.Owner != OwnerSystem || got.Mode != "ingest" {
		t.Errorf("person.alice = %+v, want system ingest row", got)
	}

	// Owner filter narrows to one tier.
	raw, err = p.handleListEntitySubscriptions(context.Background(), map[string]any{
		"owner": "battery-watcher",
	})
	if err != nil {
		t.Fatalf("owner-filtered list: %v", err)
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal filtered payload: %v", err)
	}
	if payload.Count != 1 || payload.Items[0].EntityID != "sensor.battery" {
		t.Fatalf("filtered payload = %+v, want only sensor.battery", payload)
	}

	// The retired tag filter teaches rather than silently filtering.
	if _, err := p.handleListEntitySubscriptions(context.Background(), map[string]any{
		"tag": "battery_focus",
	}); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("tag filter error = %v, want retirement teaching", err)
	}
}

func TestRemoveEntitySubscription_MissingEntityID(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	_, err := p.handleRemoveEntitySubscription(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing entity_id")
	}
	if !strings.Contains(err.Error(), "entity_id is required") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "entity_id is required")
	}
}

func TestRemoveEntitySubscription_Success(t *testing.T) {
	p, store, _ := setupWatchlistProvider(t)

	if err := store.Upsert("", looppkg.EntitySubscription{EntityID: "sensor.temperature"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	result, err := p.handleRemoveEntitySubscription(context.Background(), map[string]any{
		"entity_id": "sensor.temperature",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "sensor.temperature") {
		t.Errorf("result = %q, want to contain entity_id", result)
	}

	rows, err := store.ListOwner("")
	if err != nil {
		t.Fatalf("ListOwner: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("global rows = %v, want empty", rows)
	}
}

func TestRemoveEntitySubscription_OwnerRoutesToLoopMutator(t *testing.T) {
	p, store, mutator := setupWatchlistProvider(t)

	// A same-named global row must survive an owner-routed remove.
	if err := store.Upsert("", looppkg.EntitySubscription{EntityID: "sensor.battery"}); err != nil {
		t.Fatalf("upsert global: %v", err)
	}
	mutator.subs = map[string][]looppkg.EntitySubscription{
		"battery-watcher": {{EntityID: "sensor.battery"}},
	}

	result, err := p.handleRemoveEntitySubscription(context.Background(), map[string]any{
		"entity_id": "sensor.battery",
		"owner":     "battery-watcher",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, `Loop "battery-watcher"`) {
		t.Fatalf("result = %q, want loop-owner phrasing", result)
	}
	if len(mutator.subs["battery-watcher"]) != 0 {
		t.Fatalf("loop subs = %+v, want empty", mutator.subs["battery-watcher"])
	}
	if ids := globalEntityIDs(t, store); !slices.Equal(ids, []string{"sensor.battery"}) {
		t.Fatalf("global tier = %v, want untouched", ids)
	}
}

func TestRemoveEntitySubscription_SystemAndTagsGuards(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	if _, err := p.handleRemoveEntitySubscription(context.Background(), map[string]any{
		"entity_id": "person.alice",
		"owner":     OwnerSystem,
	}); err == nil || !strings.Contains(err.Error(), "person.track") {
		t.Fatalf("system remove error = %v, want config pointer", err)
	}

	if _, err := p.handleRemoveEntitySubscription(context.Background(), map[string]any{
		"entity_id": "sensor.battery",
		"tags":      []any{"battery_focus"},
	}); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("tags remove error = %v, want retirement teaching", err)
	}
}

// TestOwnerMutationsDoNotFireIngestRebuild pins the division of
// labor Copilot flagged on #1212: owner-addressed add/remove persist
// the spec and the app's persist hook rebuilds the ingestion filter,
// so the tool-side OnIngestChange must stay silent or the filter
// rebuilds twice per mutation.
func TestOwnerMutationsDoNotFireIngestRebuild(t *testing.T) {
	db, err := sql.Open("sqlite-thane", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewWatchlistStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	mutator := &fakeLoopMutator{subs: map[string][]looppkg.EntitySubscription{
		"battery-watcher": {{EntityID: "binary_sensor.low_battery"}},
	}}
	rebuilds := 0
	p := NewWatchlistTools(WatchlistToolsConfig{
		Store:          store,
		LoopMutator:    mutator.mutate,
		OnIngestChange: func() { rebuilds++ },
	})

	if _, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "binary_sensor.oven_door",
		"owner":     "battery-watcher",
		"mode":      "ingest",
	}); err != nil {
		t.Fatalf("owner ingest add: %v", err)
	}
	if rebuilds != 0 {
		t.Fatalf("rebuilds = %d after owner add, want 0 (the app's persist hook owns that rebuild)", rebuilds)
	}

	if _, err := p.handleRemoveEntitySubscription(context.Background(), map[string]any{
		"entity_id": "binary_sensor.low_battery",
		"owner":     "battery-watcher",
	}); err != nil {
		t.Fatalf("owner remove: %v", err)
	}
	if rebuilds != 0 {
		t.Fatalf("rebuilds = %d after owner remove, want 0", rebuilds)
	}

	// The global tier still fires: removal there is the tool's own
	// write and nothing else rebuilds on its behalf.
	if _, err := p.handleRemoveEntitySubscription(context.Background(), map[string]any{
		"entity_id": "sensor.anything",
	}); err != nil {
		t.Fatalf("global remove: %v", err)
	}
	if rebuilds != 1 {
		t.Fatalf("rebuilds = %d after global remove, want 1", rebuilds)
	}
}

// TestAddEntitySubscription_RequiresTag covers the #1213 gate at the
// tool boundary: it round-trips onto the row, surfaces in the list,
// and refuses to combine with ingest-feeding modes.
func TestAddEntitySubscription_RequiresTag(t *testing.T) {
	p, store, _ := setupWatchlistProvider(t)

	result, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id":    "sensor.stock_tank_level",
		"requires_tag": "ranch_water",
	})
	if err != nil {
		t.Fatalf("gated add: %v", err)
	}
	if !strings.Contains(result, `tag "ranch_water"`) {
		t.Fatalf("result = %q, want gate acknowledgement", result)
	}

	rows, err := store.ListOwner("")
	if err != nil {
		t.Fatalf("ListOwner: %v", err)
	}
	if len(rows) != 1 || rows[0].RequiresTag != "ranch_water" {
		t.Fatalf("rows = %+v, want requires_tag round-tripped", rows)
	}

	raw, err := p.handleListEntitySubscriptions(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var payload struct {
		Items []struct {
			EntityID    string `json:"entity_id"`
			RequiresTag string `json:"requires_tag"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].RequiresTag != "ranch_water" {
		t.Fatalf("list items = %+v, want requires_tag shown verbatim", payload.Items)
	}

	// Render-only: the gate cannot feed capture.
	if _, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id":    "binary_sensor.pump_running",
		"requires_tag": "ranch_water",
		"mode":         "ingest",
	}); err == nil || !strings.Contains(err.Error(), "render") {
		t.Fatalf("ingest+requires_tag error = %v, want render-only teaching", err)
	}
}

// TestIngestGlobsSkipsGatedRows is the defensive backstop for rows
// that bypass the tool boundary (hand-authored specs): a gated row
// never feeds the ingestion filter regardless of its mode.
func TestIngestGlobsSkipsGatedRows(t *testing.T) {
	p, store, _ := setupWatchlistProvider(t)
	_ = p

	// Write a gated ingest row directly through the store — the tool
	// boundary would reject this combination.
	if err := store.Upsert("alice-loop", looppkg.EntitySubscription{
		EntityID:    "binary_sensor.gate_open",
		Mode:        looppkg.SubscriptionModeIngest,
		RequiresTag: "ranch_water",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.Upsert("alice-loop", looppkg.EntitySubscription{
		EntityID: "binary_sensor.ungated",
		Mode:     looppkg.SubscriptionModeIngest,
	}); err != nil {
		t.Fatalf("upsert ungated: %v", err)
	}

	globs, err := store.IngestGlobs(time.Now())
	if err != nil {
		t.Fatalf("IngestGlobs: %v", err)
	}
	if !slices.Equal(globs, []string{"binary_sensor.ungated"}) {
		t.Fatalf("IngestGlobs = %v, want gated row skipped", globs)
	}
}

// TestAddEntitySubscription_TransitionOptions covers the #1210 tool
// boundary: round-trip + list surfacing, the per-subscription cap,
// the ingest-mode combo, the registry-target rejection, and the
// derived-capture rebuild signal.
func TestAddEntitySubscription_TransitionOptions(t *testing.T) {
	db, err := sql.Open("sqlite-thane", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewWatchlistStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	rebuilds := 0
	p := NewWatchlistTools(WatchlistToolsConfig{
		Store:          store,
		OnIngestChange: func() { rebuilds++ },
	})

	result, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id":                  "binary_sensor.garage_bay_3",
		"transitions":                5,
		"transitions_window_seconds": 900,
	})
	if err != nil {
		t.Fatalf("add with transitions: %v", err)
	}
	if !strings.Contains(result, "transition log") || !strings.Contains(result, "capture follows automatically") {
		t.Fatalf("result = %q, want transition-log acknowledgement", result)
	}
	if rebuilds != 1 {
		t.Fatalf("rebuilds = %d, want 1 — derived capture changes the filter", rebuilds)
	}
	globs, err := store.IngestGlobs(time.Now())
	if err != nil {
		t.Fatalf("IngestGlobs: %v", err)
	}
	if !slices.Equal(globs, []string{"binary_sensor.garage_bay_3"}) {
		t.Fatalf("IngestGlobs = %v, want derived entry", globs)
	}

	raw, err := p.handleListEntitySubscriptions(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var payload struct {
		Items []struct {
			Transitions              int `json:"transitions"`
			TransitionsWindowSeconds int `json:"transitions_window_seconds"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Transitions != 5 || payload.Items[0].TransitionsWindowSeconds != 900 {
		t.Fatalf("list items = %+v, want transition options surfaced", payload.Items)
	}

	if _, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id":   "sensor.x",
		"transitions": maxTransitionsPerSubscription + 1,
	}); err == nil || !strings.Contains(err.Error(), "capped") {
		t.Fatalf("over-cap error = %v, want cap teaching", err)
	}
	if _, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id":   "sensor.x",
		"transitions": 3,
		"mode":        "ingest",
	}); err == nil || !strings.Contains(err.Error(), "never renders") {
		t.Fatalf("ingest combo error = %v, want render teaching", err)
	}
	if _, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id":   "area:office",
		"transitions": 3,
	}); err == nil || !strings.Contains(err.Error(), "ingestion filter") {
		t.Fatalf("registry-target error = %v, want filter teaching", err)
	}
}

// TestAddEntitySubscription_WakeOptions covers the #1211 boundary:
// wake needs an owner, refuses the tag gate and registry targets,
// round-trips onto the loop's spec, and surfaces in the list.
func TestAddEntitySubscription_WakeOptions(t *testing.T) {
	p, store, mutator := setupWatchlistProvider(t)

	if _, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "binary_sensor.gate",
		"wake":      true,
	}); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("ownerless wake error = %v, want owner teaching", err)
	}
	if _, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id":    "binary_sensor.gate",
		"owner":        "watcher",
		"wake":         true,
		"requires_tag": "ranch_water",
	}); err == nil || !strings.Contains(err.Error(), "tag state") {
		t.Fatalf("wake+requires_tag error = %v, want tag-state teaching", err)
	}
	if _, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "area:office",
		"owner":     "watcher",
		"wake":      true,
	}); err == nil || !strings.Contains(err.Error(), "ingestion filter") {
		t.Fatalf("wake+registry-target error = %v, want filter teaching", err)
	}

	result, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id":             "binary_sensor.gate",
		"owner":                 "watcher",
		"wake":                  true,
		"wake_debounce_seconds": 30,
	})
	if err != nil {
		t.Fatalf("wake add: %v", err)
	}
	if !strings.Contains(result, "wake") {
		t.Fatalf("result = %q, want wake acknowledgement", result)
	}
	subs := mutator.subs["watcher"]
	if len(subs) != 1 || !subs[0].Wake || subs[0].WakeDebounceSeconds != 30 {
		t.Fatalf("loop subs = %+v, want wake options on the spec", subs)
	}

	// List surfaces wake rows (simulating the post-persist mirror).
	if err := store.Upsert("watcher", subs[0]); err != nil {
		t.Fatalf("mirror upsert: %v", err)
	}
	raw, err := p.handleListEntitySubscriptions(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var payload struct {
		Items []struct {
			Wake                bool `json:"wake"`
			WakeDebounceSeconds int  `json:"wake_debounce_seconds"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.Items) != 1 || !payload.Items[0].Wake || payload.Items[0].WakeDebounceSeconds != 30 {
		t.Fatalf("list items = %+v, want wake surfaced", payload.Items)
	}

	// Wake rows occupy the derived-capture path.
	globs, err := store.IngestGlobs(time.Now())
	if err != nil {
		t.Fatalf("IngestGlobs: %v", err)
	}
	if !slices.Equal(globs, []string{"binary_sensor.gate"}) {
		t.Fatalf("IngestGlobs = %v, want wake-derived entry", globs)
	}
}
