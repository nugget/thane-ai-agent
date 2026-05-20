package app

import (
	"context"
	"strings"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// fakeFocusStore captures AddWithOptions / RemoveWithScopes calls so
// the loop_focus_tools tests can assert on what the runtime-tool
// handlers actually do without standing up the awareness store.
type fakeFocusStore struct {
	added   []focusAdd
	removed []focusRemove
}

type focusAdd struct {
	EntityID   string
	Tags       []string
	History    []int
	TTLSeconds int
	Forecast   string
}

type focusRemove struct {
	EntityID string
	Scopes   []string
}

func (f *fakeFocusStore) AddWithOptions(entityID string, tags []string, history []int, ttlSeconds int, forecast string) error {
	f.added = append(f.added, focusAdd{
		EntityID:   entityID,
		Tags:       append([]string(nil), tags...),
		History:    append([]int(nil), history...),
		TTLSeconds: ttlSeconds,
		Forecast:   forecast,
	})
	return nil
}

func (f *fakeFocusStore) RemoveWithScopes(entityID string, scopes []string) error {
	f.removed = append(f.removed, focusRemove{
		EntityID: entityID,
		Scopes:   append([]string(nil), scopes...),
	})
	return nil
}

// TestBuildLoopFocusTools_WatchEntity exercises the watch_entity
// runtime tool's full path: the focus_tag is baked into the closure
// at hydration time, the model running an iteration calls the tool
// without naming it, and the store receives a subscription scoped to
// exactly that tag.
func TestBuildLoopFocusTools_WatchEntity(t *testing.T) {
	t.Parallel()
	store := &fakeFocusStore{}
	tools := buildLoopFocusTools(store, "loop:abc123")
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
	if len(store.added) != 1 {
		t.Fatalf("added len = %d, want 1", len(store.added))
	}
	got := store.added[0]
	if got.EntityID != "climate.upstairs" {
		t.Errorf("EntityID = %q, want climate.upstairs", got.EntityID)
	}
	// Critical: the model never typed "loop:abc123", but the store
	// received that scope because it was baked into the handler closure.
	if len(got.Tags) != 1 || got.Tags[0] != "loop:abc123" {
		t.Errorf("Tags = %v, want [loop:abc123]", got.Tags)
	}
	if len(got.History) != 2 || got.History[0] != 3600 || got.History[1] != 86400 {
		t.Errorf("History = %v, want [3600 86400]", got.History)
	}
	if got.Forecast != "hourly" {
		t.Errorf("Forecast = %q, want hourly", got.Forecast)
	}
	if got.TTLSeconds != 7200 {
		t.Errorf("TTLSeconds = %d, want 7200", got.TTLSeconds)
	}
}

// TestBuildLoopFocusTools_UnwatchEntity mirrors the watch test for
// the removal path. The remove targets only this loop's scope.
func TestBuildLoopFocusTools_UnwatchEntity(t *testing.T) {
	t.Parallel()
	store := &fakeFocusStore{}
	tools := buildLoopFocusTools(store, "loop:xyz789")

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
	if len(store.removed) != 1 {
		t.Fatalf("removed len = %d, want 1", len(store.removed))
	}
	got := store.removed[0]
	if got.EntityID != "sensor.gone" {
		t.Errorf("EntityID = %q, want sensor.gone", got.EntityID)
	}
	if len(got.Scopes) != 1 || got.Scopes[0] != "loop:xyz789" {
		t.Errorf("Scopes = %v, want [loop:xyz789]", got.Scopes)
	}
}

// TestBuildLoopFocusTools_EmptyEntityID guards the actionable-error
// path on both tools.
func TestBuildLoopFocusTools_EmptyEntityID(t *testing.T) {
	t.Parallel()
	store := &fakeFocusStore{}
	tools := buildLoopFocusTools(store, "loop:abc")

	for _, rt := range tools {
		_, err := rt.Handler(context.Background(), map[string]any{"entity_id": "   "})
		if err == nil {
			t.Errorf("%s should reject empty entity_id", rt.Name)
		}
	}
}

// TestBuildLoopFocusTools_RejectsFractionalIntegers mirrors the
// thane_curate-side coerceInt guard for the in-loop tool surface.
func TestBuildLoopFocusTools_RejectsFractionalIntegers(t *testing.T) {
	t.Parallel()
	store := &fakeFocusStore{}
	tools := buildLoopFocusTools(store, "loop:abc")
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
