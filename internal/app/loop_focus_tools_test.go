package app

import (
	"context"
	"strings"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
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

// TestBuildLoopFocusTools_RejectsFractionalIntegers mirrors the
// thane_curate-side coerceInt guard.
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
