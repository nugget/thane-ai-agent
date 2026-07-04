package app

import (
	"database/sql"
	"log/slog"
	"slices"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/awareness"
	_ "modernc.org/sqlite"
)

func setupCompileApp(t *testing.T) *App {
	t.Helper()
	db, err := sql.Open("sqlite-thane", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := awareness.NewWatchlistStore(db, nil)
	if err != nil {
		t.Fatalf("new watchlist store: %v", err)
	}
	defs, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	return &App{
		logger:                 slog.Default(),
		watchlistStore:         store,
		loopDefinitionRegistry: defs,
	}
}

// TestCompileLoopSubscriptions_MirrorsAndSweepsOrphans covers the boot
// pass: every definition's Subscriptions land as owner-keyed registry
// rows, and rows whose owner has no definition — the retired
// tag-scoped tier's leftovers — are consciously deleted. System rows
// survive the sweep.
func TestCompileLoopSubscriptions_MirrorsAndSweepsOrphans(t *testing.T) {
	a := setupCompileApp(t)

	if err := a.loopDefinitionRegistry.Upsert(looppkg.Spec{
		Name: "kitchen-watcher",
		Task: "watch the kitchen",
		Subscriptions: []looppkg.EntitySubscription{
			{EntityID: "sensor.kitchen_temp", History: []int{600}},
		},
	}, time.Now().UTC()); err != nil {
		t.Fatalf("upsert definition: %v", err)
	}

	// Orphaned rows from the retired tag tier: owners with no
	// definition (the burn-ban / msrhouston shape from prod).
	if err := a.watchlistStore.Upsert("burn-ban", looppkg.EntitySubscription{EntityID: "sensor.burn_ban_status"}); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	// System rows must survive the sweep.
	if err := a.watchlistStore.Upsert(awareness.OwnerSystem, looppkg.EntitySubscription{
		EntityID: "person.alice",
		Mode:     looppkg.SubscriptionModeIngest,
	}); err != nil {
		t.Fatalf("seed system row: %v", err)
	}
	// Global rows are untouched by definition compilation.
	if err := a.watchlistStore.Upsert("", looppkg.EntitySubscription{EntityID: "sensor.always_on"}); err != nil {
		t.Fatalf("seed global row: %v", err)
	}

	if err := a.compileLoopSubscriptions(); err != nil {
		t.Fatalf("compileLoopSubscriptions: %v", err)
	}

	rows, err := a.watchlistStore.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	got := make(map[string]string, len(rows))
	for _, row := range rows {
		got[row.EntityID] = row.Owner
	}
	want := map[string]string{
		"sensor.kitchen_temp": "kitchen-watcher",
		"person.alice":        awareness.OwnerSystem,
		"sensor.always_on":    "",
	}
	if len(got) != len(want) {
		t.Fatalf("registry rows = %v, want %v", got, want)
	}
	for entity, owner := range want {
		if got[entity] != owner {
			t.Errorf("owner[%s] = %q, want %q", entity, got[entity], owner)
		}
	}
	if _, orphanSurvived := got["sensor.burn_ban_status"]; orphanSurvived {
		t.Error("orphaned tag-tier row survived the sweep")
	}
}

// TestMirrorLoopSubscriptions_ReplacesOnPersist covers the steady-state
// projection: a spec persist replaces the owner's rows wholesale and
// fires the ingest-filter rebuild hook.
func TestMirrorLoopSubscriptions_ReplacesOnPersist(t *testing.T) {
	a := setupCompileApp(t)

	rebuilds := 0
	a.ingestFilterRebuild = func() { rebuilds++ }

	a.mirrorLoopSubscriptions(looppkg.Spec{
		Name: "kitchen-watcher",
		Subscriptions: []looppkg.EntitySubscription{
			{EntityID: "sensor.kitchen_temp"},
			{EntityID: "binary_sensor.oven_door", Mode: looppkg.SubscriptionModeIngest},
		},
	})
	if rebuilds != 1 {
		t.Fatalf("rebuilds = %d, want 1", rebuilds)
	}

	globs, err := a.watchlistStore.IngestGlobs(time.Now())
	if err != nil {
		t.Fatalf("IngestGlobs: %v", err)
	}
	if !slices.Equal(globs, []string{"binary_sensor.oven_door"}) {
		t.Fatalf("IngestGlobs = %v, want the loop-owned ingest row", globs)
	}

	// Re-mirror with a shrunk set: the dropped entity's row must go.
	a.mirrorLoopSubscriptions(looppkg.Spec{
		Name: "kitchen-watcher",
		Subscriptions: []looppkg.EntitySubscription{
			{EntityID: "sensor.kitchen_temp"},
		},
	})
	rows, err := a.watchlistStore.ListOwner("kitchen-watcher")
	if err != nil {
		t.Fatalf("ListOwner: %v", err)
	}
	if len(rows) != 1 || rows[0].EntityID != "sensor.kitchen_temp" {
		t.Fatalf("rows = %+v, want only sensor.kitchen_temp", rows)
	}
}
