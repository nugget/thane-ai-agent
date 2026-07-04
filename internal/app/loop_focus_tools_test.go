package app

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	_ "modernc.org/sqlite"
)

// fakeMutator is a thin in-memory implementation of subscriptionMutator
// for the loop_focus_tools tests. It keeps a per-loop subscription list
// so the tests can assert on the spec-shape state without standing up
// the definition registry or the live loop registry.
type fakeMutator struct {
	subs map[string][]looppkg.EntitySubscription
}

func newFakeMutator() *fakeMutator {
	return &fakeMutator{subs: make(map[string][]looppkg.EntitySubscription)}
}

func (f *fakeMutator) Mutate(_ context.Context, loopName string, mutate func([]looppkg.EntitySubscription) ([]looppkg.EntitySubscription, error)) ([]looppkg.EntitySubscription, error) {
	next, err := mutate(f.subs[loopName])
	if err != nil {
		return nil, err
	}
	f.subs[loopName] = next
	return next, nil
}

// TestBuildLoopFocusTools_WatchEntity walks watch_entity end-to-end
// through the mutator path: the model invokes the tool with no scope
// (the loop name is baked into the closure at hydration), and the
// resulting subscription lands on the loop's effective list.
func TestBuildLoopFocusTools_WatchEntity(t *testing.T) {
	t.Parallel()
	m := newFakeMutator()
	tools := buildLoopFocusToolsWithMutator("watcher_loop", m.Mutate)
	if len(tools) != 2 {
		t.Fatalf("buildLoopFocusTools returned %d tools, want 2", len(tools))
	}

	var watch *looppkg.RuntimeTool
	for i, rt := range tools {
		if rt.Name == "watch_entity" {
			watch = &tools[i]
			break
		}
	}
	if watch == nil {
		t.Fatal("watch_entity tool not generated")
	}

	out, err := watch.Handler(context.Background(), map[string]any{
		"entity_id":   "climate.upstairs",
		"history":     []any{3600, 86400},
		"forecast":    "hourly",
		"ttl_seconds": 7200,
		"include": map[string]any{
			"all": true,
		},
	})
	if err != nil {
		t.Fatalf("watch_entity handler: %v", err)
	}
	if !strings.Contains(out, "climate.upstairs") {
		t.Errorf("response %q should echo entity_id", out)
	}
	got := m.subs["watcher_loop"]
	if len(got) != 1 {
		t.Fatalf("Subscriptions len = %d, want 1: %+v", len(got), got)
	}
	if got[0].EntityID != "climate.upstairs" {
		t.Errorf("EntityID = %q, want climate.upstairs", got[0].EntityID)
	}
	if len(got[0].History) != 2 || got[0].History[0] != 3600 || got[0].History[1] != 86400 {
		t.Errorf("History = %v, want [3600 86400]", got[0].History)
	}
	if got[0].Forecast != "hourly" {
		t.Errorf("Forecast = %q, want hourly", got[0].Forecast)
	}
	if got[0].TTLSeconds != 7200 {
		t.Errorf("TTLSeconds = %d, want 7200", got[0].TTLSeconds)
	}
	if got[0].AddedAt.IsZero() {
		t.Error("AddedAt should be stamped at mutation time, got zero")
	}
	if got[0].Include == nil || !got[0].Include.Area || !got[0].Include.Device || !got[0].Include.Labels || !got[0].Include.Description {
		t.Errorf("Include = %#v, want all metadata flags", got[0].Include)
	}
}

// TestBuildLoopFocusTools_UnwatchEntity covers the symmetric remove
// path: unwatch drops the entry from the loop's subscription list.
func TestBuildLoopFocusTools_UnwatchEntity(t *testing.T) {
	t.Parallel()
	m := newFakeMutator()
	// Seed an existing subscription so unwatch has something to drop.
	m.subs["unwatch_loop"] = []looppkg.EntitySubscription{{EntityID: "sensor.gone"}}

	tools := buildLoopFocusToolsWithMutator("unwatch_loop", m.Mutate)
	var unwatch *looppkg.RuntimeTool
	for i, rt := range tools {
		if rt.Name == "unwatch_entity" {
			unwatch = &tools[i]
			break
		}
	}
	if unwatch == nil {
		t.Fatal("unwatch_entity tool not generated")
	}

	if _, err := unwatch.Handler(context.Background(), map[string]any{
		"entity_id": "sensor.gone",
	}); err != nil {
		t.Fatalf("unwatch_entity handler: %v", err)
	}
	if got := m.subs["unwatch_loop"]; len(got) != 0 {
		t.Errorf("Subscriptions after unwatch = %+v, want empty", got)
	}
}

// TestBuildLoopFocusTools_WatchReplacesExisting exercises the upsert
// semantics of watch_entity: re-invoking it for the same entity_id
// replaces options rather than duplicating the entry.
func TestBuildLoopFocusTools_WatchReplacesExisting(t *testing.T) {
	t.Parallel()
	m := newFakeMutator()
	tools := buildLoopFocusToolsWithMutator("upsert_loop", m.Mutate)
	var watch *looppkg.RuntimeTool
	for i, rt := range tools {
		if rt.Name == "watch_entity" {
			watch = &tools[i]
			break
		}
	}

	if _, err := watch.Handler(context.Background(), map[string]any{
		"entity_id": "sensor.same",
		"history":   []any{600},
	}); err != nil {
		t.Fatalf("first watch_entity: %v", err)
	}
	if _, err := watch.Handler(context.Background(), map[string]any{
		"entity_id": "sensor.same",
		"history":   []any{3600},
	}); err != nil {
		t.Fatalf("second watch_entity: %v", err)
	}
	got := m.subs["upsert_loop"]
	if len(got) != 1 {
		t.Fatalf("Subscriptions len = %d, want 1 (upsert): %+v", len(got), got)
	}
	if len(got[0].History) != 1 || got[0].History[0] != 3600 {
		t.Errorf("History = %v, want [3600] (second call's value)", got[0].History)
	}
}

// TestBuildLoopFocusTools_EmptyEntityID guards the actionable-error
// path on both tools.
func TestBuildLoopFocusTools_EmptyEntityID(t *testing.T) {
	t.Parallel()
	m := newFakeMutator()
	tools := buildLoopFocusToolsWithMutator("any_loop", m.Mutate)

	for _, rt := range tools {
		_, err := rt.Handler(context.Background(), map[string]any{"entity_id": "   "})
		if err == nil {
			t.Errorf("%s should reject empty entity_id", rt.Name)
		}
	}
}

// TestMutateLoopSubscriptionsSignalsScheduleWatcher is the
// regression test for the post-#896 audit's MED #3: any spec-
// mutation path must refire the schedule watcher so a future
// mutator that touches Conditions through this code path
// doesn't silently break wake-up timing. Today subscriptions
// don't drive schedules, so the signal is conservative; the
// architectural invariant is what's load-bearing.
func TestMutateLoopSubscriptionsSignalsScheduleWatcher(t *testing.T) {
	t.Parallel()

	reg, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	// Upsert (not seed via constructor) so the definition lands
	// as runtime-source — config-source specs are immutable and
	// mutateLoopSubscriptions rejects them.
	if err := reg.Upsert(looppkg.Spec{
		Name:         "leaf",
		Task:         "t",
		Operation:    looppkg.OperationService,
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
	}, time.Now()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	scheduleCh := make(chan struct{}, 1)
	runtime := &loopDefinitionRuntime{
		definitions: reg,
		scheduleCh:  scheduleCh,
	}
	a := &App{
		loopDefinitionRegistry: reg,
		loopDefinitionRuntime:  runtime,
	}

	_, err = a.mutateLoopSubscriptions(context.Background(), "leaf",
		func(_ []looppkg.EntitySubscription) ([]looppkg.EntitySubscription, error) {
			return []looppkg.EntitySubscription{{EntityID: "sensor.foo"}}, nil
		},
	)
	if err != nil {
		t.Fatalf("mutateLoopSubscriptions: %v", err)
	}

	// Coalesced signal channel: at least one value must have
	// landed. We use a non-blocking receive so a missing signal
	// fails fast rather than hanging the test.
	select {
	case <-scheduleCh:
		// good
	default:
		t.Fatal("schedule watcher was not signaled after spec mutation; the 'any spec write refires the schedule watcher' invariant is broken")
	}
}

// TestMutateLoopSubscriptionsWritesPersistedAndLive is the
// regression test for the post-#896 audit's LOW #4: the dual-
// walker model (live effectiveState vs. persisted
// EvaluateEffectiveConditions) only agrees when both surfaces
// reflect the mutation. This test wires a real
// opstate-backed loopDefinitionStore (in-memory SQLite) so the
// persistLoopDefinition call is exercised and observable —
// removing or reordering the persist step would leave the
// store empty, failing the disk-side assertion. The strict
// "persist BEFORE live patch" ordering can't be observed
// from a single in-process run (no crash-injection here), but
// asserting that BOTH writes occurred catches the most likely
// regression: a future refactor that drops persistence and
// only patches live state.
func TestMutateLoopSubscriptionsWritesPersistedAndLive(t *testing.T) {
	t.Parallel()

	reg, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	if err := reg.Upsert(looppkg.Spec{
		Name:         "leaf",
		Task:         "t",
		Operation:    looppkg.OperationService,
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
	}, time.Now()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	loops := looppkg.NewRegistry()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		loops.ShutdownAll(shutdownCtx)
	})
	core, err := looppkg.New(looppkg.Config{Name: looppkg.CoreLoopName, Operation: looppkg.OperationContainer}, looppkg.Deps{})
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	if err := loops.Register(core); err != nil {
		t.Fatalf("register core: %v", err)
	}
	live, err := looppkg.New(looppkg.Config{Name: "leaf", Task: "t"}, looppkg.Deps{Runner: testLoopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := loops.Register(live); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	// Real opstate-backed store on an in-memory SQLite — so
	// persistLoopDefinition is not a no-op and a future
	// refactor that drops the persist call would leave the
	// disk-side assertion below empty.
	db, err := sql.Open("sqlite-thane", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	opStore, err := opstate.NewStore(db, nil)
	if err != nil {
		t.Fatalf("opstate.NewStore: %v", err)
	}
	store := newLoopDefinitionStore(opStore)

	runtime := &loopDefinitionRuntime{
		definitions: reg,
		scheduleCh:  make(chan struct{}, 1),
	}
	a := &App{
		loopDefinitionRegistry: reg,
		loopDefinitionRuntime:  runtime,
		loopRegistry:           loops,
		loopDefinitionStore:    store,
	}

	_, err = a.mutateLoopSubscriptions(context.Background(), "leaf",
		func(_ []looppkg.EntitySubscription) ([]looppkg.EntitySubscription, error) {
			return []looppkg.EntitySubscription{{EntityID: "sensor.foo"}}, nil
		},
	)
	if err != nil {
		t.Fatalf("mutateLoopSubscriptions: %v", err)
	}

	// Disk-side assertion: read the store directly. Failing here
	// means persistLoopDefinition was skipped or didn't actually
	// reach the backing opstate — the in-memory DefinitionRegistry
	// would still show the change, so the previous version of
	// this test would have passed and missed the regression.
	persisted, err := opStore.Get(loopDefinitionRegistryNamespace, "leaf")
	if err != nil {
		t.Fatalf("opStore.Get: %v", err)
	}
	if persisted == "" {
		t.Fatal("opStore has no row for leaf — persistLoopDefinition was skipped")
	}
	if !strings.Contains(persisted, "sensor.foo") {
		t.Errorf("persisted payload missing sensor.foo: %s", persisted)
	}

	// In-memory definition-registry view: same content via the
	// snapshot path most callers use.
	snap := reg.Snapshot()
	if snap == nil {
		t.Fatal("definition registry Snapshot returned nil")
	}
	var found bool
	for _, def := range snap.Definitions {
		if def.Name != "leaf" {
			continue
		}
		found = true
		if len(def.Spec.Subscriptions) != 1 || def.Spec.Subscriptions[0].EntityID != "sensor.foo" {
			t.Errorf("registry-snapshot Subscriptions = %v, want [sensor.foo]", def.Spec.Subscriptions)
		}
	}
	if !found {
		t.Fatal("leaf definition missing from snapshot after mutation")
	}

	// Live view: same change should be visible on the running
	// loop. All three surfaces (disk, registry-snapshot, live
	// loop) agreeing is the invariant the dual-walker model
	// relies on.
	liveSubs := live.Subscriptions()
	if len(liveSubs) != 1 || liveSubs[0].EntityID != "sensor.foo" {
		t.Errorf("live Subscriptions = %v, want [sensor.foo]", liveSubs)
	}
}

// TestBuildLoopFocusTools_RejectsFractionalIntegers mirrors the
// thane_loop_create-side coerceInt guard.
func TestBuildLoopFocusTools_RejectsFractionalIntegers(t *testing.T) {
	t.Parallel()
	m := newFakeMutator()
	tools := buildLoopFocusToolsWithMutator("any_loop", m.Mutate)
	var watch *looppkg.RuntimeTool
	for i, rt := range tools {
		if rt.Name == "watch_entity" {
			watch = &tools[i]
			break
		}
	}

	_, err := watch.Handler(context.Background(), map[string]any{
		"entity_id":   "sensor.foo",
		"ttl_seconds": 3600.5,
	})
	if err == nil {
		t.Fatal("expected fractional ttl_seconds to be rejected")
	}
	if !strings.Contains(err.Error(), "fractional") {
		t.Errorf("error %q should mention fractional", err)
	}
}

// TestBuildLoopFocusTools_WatchEntityModeAndSelfOnly covers the #1209
// vocabulary additions on the in-loop door: mode lands canonicalized
// on the subscription, self_only carries through, registry targets
// are rejected for ingest-feeding modes, and unknown modes teach.
func TestBuildLoopFocusTools_WatchEntityModeAndSelfOnly(t *testing.T) {
	t.Parallel()
	m := newFakeMutator()
	tools := buildLoopFocusToolsWithMutator("watcher_loop", m.Mutate)

	var watch *looppkg.RuntimeTool
	for i, rt := range tools {
		if rt.Name == "watch_entity" {
			watch = &tools[i]
			break
		}
	}
	if watch == nil {
		t.Fatal("watch_entity tool not generated")
	}

	if _, err := watch.Handler(context.Background(), map[string]any{
		"entity_id": "binary_sensor.*door*",
		"mode":      "ingest",
		"self_only": true,
	}); err != nil {
		t.Fatalf("watch with mode/self_only: %v", err)
	}
	subs := m.subs["watcher_loop"]
	if len(subs) != 1 {
		t.Fatalf("subs = %+v, want 1 entry", subs)
	}
	if subs[0].Mode != looppkg.SubscriptionModeIngest {
		t.Errorf("mode = %q, want ingest", subs[0].Mode)
	}
	if !subs[0].SelfOnly {
		t.Error("self_only lost on the way to the spec")
	}

	// mode render canonicalizes to the empty string on the spec.
	if _, err := watch.Handler(context.Background(), map[string]any{
		"entity_id": "sensor.plain",
		"mode":      "render",
	}); err != nil {
		t.Fatalf("watch with mode render: %v", err)
	}
	for _, sub := range m.subs["watcher_loop"] {
		if sub.EntityID == "sensor.plain" && sub.Mode != "" {
			t.Errorf("render mode stored as %q, want canonical empty", sub.Mode)
		}
	}

	if _, err := watch.Handler(context.Background(), map[string]any{
		"entity_id": "area:office",
		"mode":      "ingest",
	}); err == nil || !strings.Contains(err.Error(), "ingestion filter") {
		t.Fatalf("registry-target ingest error = %v, want ingestion-filter guidance", err)
	}

	if _, err := watch.Handler(context.Background(), map[string]any{
		"entity_id": "sensor.x",
		"mode":      "firehose",
	}); err == nil || !strings.Contains(err.Error(), "mode must be one of") {
		t.Fatalf("unknown mode error = %v, want vocabulary teaching", err)
	}
}
