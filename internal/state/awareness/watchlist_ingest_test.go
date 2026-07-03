package awareness

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
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

// The ingestion registry (#1192): ingest/both rows feed the state
// watcher's filter; render rows and expired rows do not.
func TestIngestGlobs(t *testing.T) {
	store := newIngestStore(t)
	now := time.Now()

	if err := store.AddWithOptions("binary_sensor.*_door", nil, nil, 0, "", SubscriptionModeIngest); err != nil {
		t.Fatalf("add ingest glob: %v", err)
	}
	if err := store.AddWithOptions("lock.front", nil, nil, 0, "", SubscriptionModeBoth); err != nil {
		t.Fatalf("add both: %v", err)
	}
	if err := store.Add("sensor.render_only"); err != nil {
		t.Fatalf("add render: %v", err)
	}
	// Expired ingest row drops out of the rebuild.
	if err := store.AddWithOptions("sensor.expired", nil, nil, 1, "", SubscriptionModeIngest); err != nil {
		t.Fatalf("add expiring: %v", err)
	}

	globs, err := store.IngestGlobs(now.Add(2 * time.Second))
	if err != nil {
		t.Fatalf("IngestGlobs: %v", err)
	}
	got := strings.Join(globs, ",")
	if !strings.Contains(got, "binary_sensor.*_door") || !strings.Contains(got, "lock.front") {
		t.Errorf("globs = %v, want ingest + both rows", globs)
	}
	if strings.Contains(got, "render_only") {
		t.Errorf("render-only row leaked into ingest globs: %v", globs)
	}
	if strings.Contains(got, "expired") {
		t.Errorf("expired row leaked into ingest globs: %v", globs)
	}
}

// Mode survives the options round trip and defaults to render for
// pre-mode rows (absent field).
func TestSubscriptionModeRoundTrip(t *testing.T) {
	store := newIngestStore(t)
	if err := store.AddWithOptions("sensor.a", nil, nil, 0, "", SubscriptionModeIngest); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := store.Add("sensor.b"); err != nil {
		t.Fatalf("add plain: %v", err)
	}
	subs, err := store.ListUntaggedSubscriptions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	modes := map[string]string{}
	for _, s := range subs {
		modes[s.EntityID] = s.Mode
	}
	if modes["sensor.a"] != SubscriptionModeIngest {
		t.Errorf("sensor.a mode = %q, want ingest", modes["sensor.a"])
	}
	if modes["sensor.b"] != SubscriptionModeRender {
		t.Errorf("sensor.b mode = %q, want render default", modes["sensor.b"])
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
	}); err == nil || !strings.Contains(err.Error(), "render, ingest, or both") {
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
}

// Ingest-only rows feed the push pipeline, not the per-turn render: the
// watchlist context provider must skip them.
func TestWatchlistProviderSkipsIngestOnlyRows(t *testing.T) {
	store := newIngestStore(t)
	if err := store.AddWithOptions("sensor.ingest_only", nil, nil, 0, "", SubscriptionModeIngest); err != nil {
		t.Fatalf("add: %v", err)
	}
	subs, err := store.ListUntaggedSubscriptions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(subs) != 1 || subs[0].Mode != SubscriptionModeIngest {
		t.Fatalf("subs = %+v, want one ingest row", subs)
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
