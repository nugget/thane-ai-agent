package awareness

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	_ "modernc.org/sqlite"
)

// setupLoopSubProvider builds a LoopSubscriptionProvider with an
// in-memory watchlist store, an in-memory loop registry, and the
// fakeHA stub used by the rest of the awareness tests.
func setupLoopSubProvider(t *testing.T) (*LoopSubscriptionProvider, *WatchlistStore, *looppkg.Registry, *fakeHA) {
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

	ha := &fakeHA{
		states: map[string]*homeassistant.State{
			"sensor.shared": {EntityID: "sensor.shared", State: "1"},
			"sensor.loop":   {EntityID: "sensor.loop", State: "2"},
		},
	}
	reg := looppkg.NewRegistry()

	p := NewLoopSubscriptionProvider(reg, store, ha, slog.Default())
	return p, store, reg, ha
}

// TestLoopSubscriptionProviderSkipsAlwaysVisibleEntities is the
// regression test for the audit's HIGH #5 finding: an entity
// present in BOTH the always-visible store and the loop's
// effective subscriptions was being rendered twice (one block by
// WatchlistProvider, one by LoopSubscriptionProvider) and
// triggering two HA fetches per turn. The loop-scoped renderer
// now filters out entity_ids the always-visible store already
// carries — the always-on rendering wins because it would appear
// in every loop's context regardless.
func TestLoopSubscriptionProviderSkipsAlwaysVisibleEntities(t *testing.T) {
	t.Parallel()

	p, store, reg, _ := setupLoopSubProvider(t)

	// Always-visible subscription via the store (no scope).
	if err := store.Add("sensor.shared"); err != nil {
		t.Fatalf("seed always-visible: %v", err)
	}

	// Loop with two subs: one that overlaps the always-visible
	// entry, one that's loop-only.
	leaf, err := looppkg.New(looppkg.Config{
		Name: "leaf",
		Task: "t",
		Subscriptions: []looppkg.EntitySubscription{
			{EntityID: "sensor.shared"},
			{EntityID: "sensor.loop"},
		},
	}, looppkg.Deps{Runner: noopRunnerForLoopSub{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := reg.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	ctx := looppkg.WithLoopIDForTest(context.Background(), leaf.ID())
	out, err := p.TagContext(ctx, agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}

	// The loop-scoped block should mention sensor.loop but NOT
	// sensor.shared (the always-visible block covers it).
	if !strings.Contains(out, "sensor.loop") {
		t.Errorf("loop-scoped render missing sensor.loop: %q", out)
	}
	if strings.Contains(out, "sensor.shared") {
		t.Errorf("loop-scoped render leaked sensor.shared (already always-visible): %q", out)
	}

	// The output-text check above is what enforces the dedup;
	// fakeHA doesn't track GetState call counts, so asserting on
	// the rendered block is the strongest signal we have without
	// extending the stub. Promoting that to a fetch-count
	// assertion (by recording GetState invocations on fakeHA) is
	// a worthwhile follow-up if the provider's render path ever
	// grows more side effects.
}

// noopRunnerForLoopSub satisfies the Runner interface so a loop
// with a Task can be constructed; never actually called by these
// tests because we don't invoke the runner.
type noopRunnerForLoopSub struct{}

func (noopRunnerForLoopSub) Run(_ context.Context, _ looppkg.Request, _ looppkg.StreamCallback) (*looppkg.Response, error) {
	return &looppkg.Response{}, nil
}
