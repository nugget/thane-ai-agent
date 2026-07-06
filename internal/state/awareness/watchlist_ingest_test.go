package awareness

import (
	"context"
	"database/sql"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func newIngestStore(t *testing.T) *WatchlistStore {
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

// The ingestion registry (#1192, widened by #1209): ingest/both rows
// from every owner feed the state watcher's filter; render rows,
// expired rows, and registry targets do not.
func TestIngestGlobs(t *testing.T) {
	store := newIngestStore(t)
	now := time.Now()

	seed := []struct {
		owner string
		sub   looppkg.EntitySubscription
	}{
		{OwnerCore, looppkg.EntitySubscription{EntityID: "binary_sensor.*_door", Mode: looppkg.SubscriptionModeIngest}},
		{OwnerCore, looppkg.EntitySubscription{EntityID: "lock.front", Mode: looppkg.SubscriptionModeBoth}},
		{OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.render_only"}},
		// Expired ingest row drops out of the rebuild.
		{OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.expired", Mode: looppkg.SubscriptionModeIngest, TTLSeconds: 1, AddedAt: now}},
		// Loop-owned ingest rows feed the filter too — #1209 widened the
		// read from the always-visible tier to every owner.
		{"alice", looppkg.EntitySubscription{EntityID: "sensor.loop_owned", Mode: looppkg.SubscriptionModeIngest}},
		// Registry targets can't feed the EntityFilter (ids and globs
		// only); the store tolerates the row but the rebuild skips it.
		{"alice", looppkg.EntitySubscription{EntityID: "area:office", Mode: looppkg.SubscriptionModeIngest}},
	}
	for _, s := range seed {
		if err := store.Upsert(s.owner, s.sub); err != nil {
			t.Fatalf("upsert %q/%s: %v", s.owner, s.sub.EntityID, err)
		}
	}

	globs, err := store.IngestGlobs(now.Add(2 * time.Second))
	if err != nil {
		t.Fatalf("IngestGlobs: %v", err)
	}
	got := strings.Join(globs, ",")
	for _, want := range []string{"binary_sensor.*_door", "lock.front", "sensor.loop_owned"} {
		if !strings.Contains(got, want) {
			t.Errorf("globs = %v, want %s (ingest/both rows across all owners)", globs, want)
		}
	}
	for _, absent := range []string{"render_only", "expired", "area:office"} {
		if strings.Contains(got, absent) {
			t.Errorf("%s leaked into ingest globs: %v", absent, globs)
		}
	}
}

// Mode survives the options round trip; render is stored and decoded
// as the canonical empty string (#1209), so a pre-mode row (absent
// field) and an explicit render row are indistinguishable.
func TestSubscriptionModeRoundTrip(t *testing.T) {
	store := newIngestStore(t)
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.a", Mode: looppkg.SubscriptionModeIngest}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.b"}); err != nil {
		t.Fatalf("add plain: %v", err)
	}
	rows, err := store.ListOwner(OwnerCore)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	modes := map[string]string{}
	for _, row := range rows {
		modes[row.EntityID] = row.Mode
	}
	if modes["sensor.a"] != looppkg.SubscriptionModeIngest {
		t.Errorf("sensor.a mode = %q, want ingest", modes["sensor.a"])
	}
	if modes["sensor.b"] != "" {
		t.Errorf("sensor.b mode = %q, want \"\" (the canonical stored form of render)", modes["sensor.b"])
	}
}

// Ingest-only rows must not render per-turn state (they feed the
// state-change window instead); the tool result and list expose mode.
func TestIngestModeToolFlow(t *testing.T) {
	client := expansionRegistryClient()
	fired := 0
	db, err := sql.Open("sqlite-thane", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewWatchlistStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	p := NewWatchlistTools(WatchlistToolsConfig{
		Store:          store,
		Registry:       client,
		OnIngestChange: func() { fired++ },
	})
	ctx := context.Background()

	out, err := p.handleAddEntitySubscription(ctx, map[string]any{
		"entity_id": "binary_sensor.*door*",
		"mode":      "ingest",
	})
	if err != nil {
		t.Fatalf("add ingest: %v", err)
	}
	if !strings.Contains(out, "mode: ingest") || !strings.Contains(out, "recent-state-changes window") {
		t.Errorf("result should describe ingest mode: %s", out)
	}
	if fired != 1 {
		t.Errorf("OnIngestChange fired %d times, want 1", fired)
	}

	// The tag tier is retired (#1209): any tags argument is rejected
	// with a teaching error that points at owner instead.
	if _, err := p.handleAddEntitySubscription(ctx, map[string]any{
		"entity_id": "sensor.tagged",
		"mode":      "ingest",
		"tags":      []any{"focus"},
	}); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Errorf("tags parameter should be rejected as retired, got: %v", err)
	}

	// Registry targets can't feed the ingestion filter.
	if _, err := p.handleAddEntitySubscription(ctx, map[string]any{
		"entity_id": "area:office",
		"mode":      "ingest",
	}); err == nil || !strings.Contains(err.Error(), "entity ids and globs only") {
		t.Errorf("registry target in ingest mode should error, got: %v", err)
	}

	// Unknown mode is rejected.
	if _, err := p.handleAddEntitySubscription(ctx, map[string]any{
		"entity_id": "sensor.x",
		"mode":      "firehose",
	}); err == nil || !strings.Contains(err.Error(), "render, ingest, both") {
		t.Errorf("bad mode should error, got: %v", err)
	}

	// Removal also triggers a rebuild.
	if _, err := p.handleRemoveEntitySubscription(ctx, map[string]any{
		"entity_id": "binary_sensor.*door*",
	}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if fired != 2 {
		t.Errorf("OnIngestChange fired %d times after remove, want 2", fired)
	}
}

// The ingest registry cap: the guardrail against a runaway subscription
// loop flooding the ingestion filter.
func TestIngestCap(t *testing.T) {
	store := newIngestStore(t)
	p := NewWatchlistTools(WatchlistToolsConfig{Store: store})
	ctx := context.Background()
	for i := 0; i < maxIngestEntries; i++ {
		if _, err := p.handleAddEntitySubscription(ctx, map[string]any{
			"entity_id": "sensor.ingest_" + strings.Repeat("x", 1) + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)),
			"mode":      "ingest",
		}); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if _, err := p.handleAddEntitySubscription(ctx, map[string]any{
		"entity_id": "sensor.one_too_many",
		"mode":      "ingest",
	}); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Errorf("over-cap add should error, got: %v", err)
	}

	// Re-adding an existing entry at the cap is an in-place update,
	// not growth — it must pass.
	if _, err := p.handleAddEntitySubscription(ctx, map[string]any{
		"entity_id": "sensor.ingest_xaa",
		"mode":      "ingest",
		"history":   []any{600},
	}); err != nil {
		t.Errorf("re-add at cap should succeed as an update: %v", err)
	}
}

// An unrecognized stored mode degrades to render — decoded as the
// canonical empty string (#1209) — keeping the legacy-tolerant read
// posture the store has always had.
func TestUnknownStoredModeDecodesAsRender(t *testing.T) {
	store := newIngestStore(t)
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.future"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := store.db.Exec(
		`UPDATE watched_entity_subscriptions SET options = '{"mode":"telepathy"}' WHERE entity_id = 'sensor.future'`); err != nil {
		t.Fatalf("plant unknown mode: %v", err)
	}
	rows, err := store.ListOwner(OwnerCore)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	if rows[0].Mode != "" || !rows[0].RendersState() {
		t.Errorf("unknown mode decoded as %q, want the canonical render form \"\"", rows[0].Mode)
	}
}

// Ingest-only rows feed the push pipeline, not the per-turn render: the
// watchlist context provider must skip them.
func TestWatchlistProviderSkipsIngestOnlyRows(t *testing.T) {
	store := newIngestStore(t)
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.ingest_only", Mode: looppkg.SubscriptionModeIngest}); err != nil {
		t.Fatalf("add: %v", err)
	}
	rows, err := store.ListOwner(OwnerCore)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].Mode != looppkg.SubscriptionModeIngest {
		t.Fatalf("rows = %+v, want one ingest row", rows)
	}
	// Provider with a nil HA client renders nothing anyway on fetch,
	// but the skip must happen before any state fetch: an ingest-only
	// watchlist yields an empty context block.
	p := NewWatchlistProvider(store, nil, nil)
	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if got != "" {
		t.Errorf("ingest-only watchlist rendered context: %q", got)
	}
}

// TestIngestGlobsDerivesFromTransitionLogs covers the #1210 capture
// insight: a subscription that renders a transition log needs the
// stream, so its target joins the filter with no user-facing mode —
// including gated logs (capture unconditional, render gated) — while
// gated mode-based capture stays excluded (#1213 backstop).
func TestIngestGlobsDerivesFromTransitionLogs(t *testing.T) {
	store := newIngestStore(t)

	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{
		EntityID:    "binary_sensor.garage_bay_3",
		Transitions: 5,
	}); err != nil {
		t.Fatalf("upsert transitions row: %v", err)
	}
	if err := store.Upsert("alice-loop", looppkg.EntitySubscription{
		EntityID:                 "sensor.stock_tank_level",
		TransitionsWindowSeconds: 900,
		RequiresTag:              "ranch_water",
	}); err != nil {
		t.Fatalf("upsert gated windowed row: %v", err)
	}
	// Gated MODE-based capture is the #1213 backstop exclusion.
	if err := store.Upsert("bob-loop", looppkg.EntitySubscription{
		EntityID:    "sensor.gated_mode",
		Mode:        looppkg.SubscriptionModeBoth,
		RequiresTag: "ranch_water",
	}); err != nil {
		t.Fatalf("upsert gated mode row: %v", err)
	}
	// Plain render row: no capture.
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.render_only"}); err != nil {
		t.Fatalf("upsert render row: %v", err)
	}

	globs, err := store.IngestGlobs(time.Now())
	if err != nil {
		t.Fatalf("IngestGlobs: %v", err)
	}
	sort.Strings(globs)
	want := []string{"binary_sensor.garage_bay_3", "sensor.stock_tank_level"}
	if !slices.Equal(globs, want) {
		t.Fatalf("IngestGlobs = %v, want %v", globs, want)
	}
}
