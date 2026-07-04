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

	// Always-visible subscription via the store (owner "").
	if err := store.Upsert("", looppkg.EntitySubscription{EntityID: "sensor.shared"}); err != nil {
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

// TestLoopSubscriptionProviderHonorsRequiresTag covers the #1213
// render gate on the loop path: a gated subscription renders only
// while its capability tag is active in the iteration's context.
func TestLoopSubscriptionProviderHonorsRequiresTag(t *testing.T) {
	t.Parallel()

	p, _, reg, ha := setupLoopSubProvider(t)
	ha.states["sensor.lensed"] = &homeassistant.State{EntityID: "sensor.lensed", State: "3"}

	leaf, err := looppkg.New(looppkg.Config{
		Name: "leaf",
		Task: "t",
		Subscriptions: []looppkg.EntitySubscription{
			{EntityID: "sensor.loop"},
			{EntityID: "sensor.lensed", RequiresTag: "ranch_water"},
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
		t.Fatalf("TagContext (tag off): %v", err)
	}
	if strings.Contains(out, "sensor.lensed") {
		t.Errorf("gated subscription rendered with its tag inactive: %q", out)
	}
	if !strings.Contains(out, "sensor.loop") {
		t.Errorf("ungated subscription missing: %q", out)
	}

	out, err = p.TagContext(ctx, agentctx.ContextRequest{ActiveTags: map[string]bool{"ranch_water": true}})
	if err != nil {
		t.Fatalf("TagContext (tag on): %v", err)
	}
	if !strings.Contains(out, "sensor.lensed") {
		t.Errorf("gated subscription missing with its tag active: %q", out)
	}
}

// TestLoopSubscriptionProviderDedupIgnoresGatedOffGlobalRows pins the
// dedup trap recorded on #1213: a gated always-visible row that will
// NOT render this turn must not suppress the loop's own ungated
// subscription for the same entity — otherwise the entity would
// vanish from context entirely while the conversation's tag is off.
func TestLoopSubscriptionProviderDedupIgnoresGatedOffGlobalRows(t *testing.T) {
	t.Parallel()

	p, store, reg, _ := setupLoopSubProvider(t)

	// Always-visible row gated on a tag.
	if err := store.Upsert("", looppkg.EntitySubscription{EntityID: "sensor.shared", RequiresTag: "ranch_water"}); err != nil {
		t.Fatalf("seed gated global: %v", err)
	}

	leaf, err := looppkg.New(looppkg.Config{
		Name: "leaf",
		Task: "t",
		Subscriptions: []looppkg.EntitySubscription{
			{EntityID: "sensor.shared"},
		},
	}, looppkg.Deps{Runner: noopRunnerForLoopSub{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := reg.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}
	ctx := looppkg.WithLoopIDForTest(context.Background(), leaf.ID())

	// Tag off: the global tier renders nothing for the entity, so the
	// loop's own subscription must show it.
	out, err := p.TagContext(ctx, agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext (tag off): %v", err)
	}
	if !strings.Contains(out, "sensor.shared") {
		t.Errorf("gated-off global row suppressed the loop's own subscription: %q", out)
	}

	// Tag on: the global tier renders it, so the loop must dedup.
	out, err = p.TagContext(ctx, agentctx.ContextRequest{ActiveTags: map[string]bool{"ranch_water": true}})
	if err != nil {
		t.Fatalf("TagContext (tag on): %v", err)
	}
	if strings.Contains(out, "sensor.shared") {
		t.Errorf("loop double-rendered an entity the open-gated global tier covers: %q", out)
	}
}

// TestLoopSubscriptionProviderGatedLeafShadowsUngatedAncestor pins the
// first-wins/gate composition decided on #1213: the closest
// declaration wins INCLUDING its conditions, so a leaf's gated entry
// shadows a container's ungated one and the entity is absent while
// the leaf's tag is off.
func TestLoopSubscriptionProviderGatedLeafShadowsUngatedAncestor(t *testing.T) {
	t.Parallel()

	p, _, reg, _ := setupLoopSubProvider(t)

	root, err := looppkg.New(looppkg.Config{
		Name:      "root_container",
		Operation: looppkg.OperationContainer,
		Subscriptions: []looppkg.EntitySubscription{
			{EntityID: "sensor.shared"},
		},
	}, looppkg.Deps{})
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	if err := reg.Register(root); err != nil {
		t.Fatalf("register root: %v", err)
	}
	leaf, err := looppkg.New(looppkg.Config{
		Name:     "leaf",
		Task:     "t",
		ParentID: root.ID(),
		Subscriptions: []looppkg.EntitySubscription{
			{EntityID: "sensor.shared", RequiresTag: "ranch_water"},
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
		t.Fatalf("TagContext (tag off): %v", err)
	}
	if strings.Contains(out, "sensor.shared") {
		t.Errorf("shadowed entity rendered while the leaf's gate is closed; first-wins should include conditions: %q", out)
	}

	out, err = p.TagContext(ctx, agentctx.ContextRequest{ActiveTags: map[string]bool{"ranch_water": true}})
	if err != nil {
		t.Fatalf("TagContext (tag on): %v", err)
	}
	if !strings.Contains(out, "sensor.shared") {
		t.Errorf("gated leaf declaration missing with its tag active: %q", out)
	}
}

// TestLoopSubscriptionProviderDedupIgnoresNonRenderingGlobalRows pins
// the Copilot finding on #1214: an always-visible row that never
// renders — ingest-only mode — must not suppress a loop's own render
// of the same entity. Before GlobalEntityGates filtered on
// RendersState, the entity would silently vanish from loop context
// whenever the global tier used it for capture only.
func TestLoopSubscriptionProviderDedupIgnoresNonRenderingGlobalRows(t *testing.T) {
	t.Parallel()

	p, store, reg, _ := setupLoopSubProvider(t)

	// Global row that feeds capture only; WatchlistProvider never
	// renders it.
	if err := store.Upsert("", looppkg.EntitySubscription{
		EntityID: "sensor.shared",
		Mode:     looppkg.SubscriptionModeIngest,
	}); err != nil {
		t.Fatalf("seed ingest-only global: %v", err)
	}

	leaf, err := looppkg.New(looppkg.Config{
		Name: "leaf",
		Task: "t",
		Subscriptions: []looppkg.EntitySubscription{
			{EntityID: "sensor.shared"},
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
	if !strings.Contains(out, "sensor.shared") {
		t.Errorf("ingest-only global row suppressed the loop's render: %q", out)
	}

	// mode both DOES render globally, so the dedup must apply.
	if err := store.Upsert("", looppkg.EntitySubscription{
		EntityID: "sensor.shared",
		Mode:     looppkg.SubscriptionModeBoth,
	}); err != nil {
		t.Fatalf("upgrade to both: %v", err)
	}
	out, err = p.TagContext(ctx, agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext (both): %v", err)
	}
	if strings.Contains(out, "sensor.shared") {
		t.Errorf("loop double-rendered an entity the global tier renders (mode both): %q", out)
	}
}
