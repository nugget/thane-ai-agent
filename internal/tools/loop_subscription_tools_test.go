package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestLoopUpdateEntitySubscriptions_AddRemove exercises the happy
// path: a loop created by thane_curate has its watch set adjusted
// via the external tool, and both add and remove arrive at the
// underlying store with the resolved focus_tag baked in.
func TestLoopUpdateEntitySubscriptions_AddRemove(t *testing.T) {
	t.Parallel()
	rig := newCurateTestRig(t)

	// Seed a curate loop with one entity so we have something to remove.
	if _, err := rig.tool.Handler(context.Background(), map[string]any{
		"name":    "watcher",
		"intent":  "watch things",
		"cadence": "hourly",
		"output": map[string]any{
			"mode":     "journal",
			"document": "kb:watch/log.md",
		},
		"entities": []any{
			map[string]any{"entity_id": "sensor.old"},
		},
	}); err != nil {
		t.Fatalf("thane_curate seed: %v", err)
	}
	spec := rig.findCurateSpec(t, "watcher")
	focusTag := spec.Metadata["focus_tag"]
	if focusTag == "" {
		t.Fatal("focus_tag missing after seed")
	}

	// Reset captures so we isolate the update call.
	rig.subStore.added = nil
	rig.subStore.removed = nil
	rig.subStore.wiped = nil

	updateTool := rig.reg.Get("loop_update_entity_subscriptions")
	if updateTool == nil {
		t.Fatal("loop_update_entity_subscriptions not registered")
	}

	result, err := updateTool.Handler(context.Background(), map[string]any{
		"name": "watcher",
		"add": []any{
			map[string]any{"entity_id": "sensor.new", "history": []any{600}},
			map[string]any{"entity_id": "weather.home", "forecast": "hourly"},
		},
		"remove": []any{"sensor.old"},
	})
	if err != nil {
		t.Fatalf("loop_update_entity_subscriptions: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status = %v, want ok", resp["status"])
	}
	if resp["focus_tag"] != focusTag {
		t.Errorf("response focus_tag = %v, want %q", resp["focus_tag"], focusTag)
	}
	if resp["added"] != float64(2) || resp["removed"] != float64(1) {
		t.Errorf("counts wrong: added=%v removed=%v", resp["added"], resp["removed"])
	}

	// Removes are applied before adds (so re-adding the same entity
	// works cleanly in a single call).
	if len(rig.subStore.removed) != 1 {
		t.Fatalf("removed len = %d, want 1", len(rig.subStore.removed))
	}
	if rig.subStore.removed[0].EntityID != "sensor.old" {
		t.Errorf("removed[0].EntityID = %q, want sensor.old", rig.subStore.removed[0].EntityID)
	}
	if len(rig.subStore.removed[0].Scopes) != 1 || rig.subStore.removed[0].Scopes[0] != focusTag {
		t.Errorf("removed[0].Scopes = %v, want [%q]", rig.subStore.removed[0].Scopes, focusTag)
	}

	if len(rig.subStore.added) != 2 {
		t.Fatalf("added len = %d, want 2", len(rig.subStore.added))
	}
	for i, sub := range rig.subStore.added {
		if len(sub.Tags) != 1 || sub.Tags[0] != focusTag {
			t.Errorf("added[%d].Tags = %v, want [%q]", i, sub.Tags, focusTag)
		}
	}
	if rig.subStore.added[1].Forecast != "hourly" {
		t.Errorf("added[1].Forecast = %q, want hourly", rig.subStore.added[1].Forecast)
	}
}

// TestLoopUpdateEntitySubscriptions_UnknownLoop covers the actionable-
// error path when the named loop isn't in the registry.
func TestLoopUpdateEntitySubscriptions_UnknownLoop(t *testing.T) {
	t.Parallel()
	rig := newCurateTestRig(t)

	tool := rig.reg.Get("loop_update_entity_subscriptions")
	if tool == nil {
		t.Fatal("loop_update_entity_subscriptions not registered")
	}
	_, err := tool.Handler(context.Background(), map[string]any{
		"name": "no_such_loop",
		"add":  []any{map[string]any{"entity_id": "sensor.foo"}},
	})
	if err == nil {
		t.Fatal("expected error for unknown loop")
	}
	if !strings.Contains(err.Error(), "no_such_loop") {
		t.Errorf("error %q should name the missing loop", err)
	}
}

// TestLoopUpdateEntitySubscriptions_RejectsLoopWithoutFocusTag covers
// the case where the named loop exists but predates the focus_tag
// machinery (e.g. an older loop_definition_set spec without the
// metadata key). The model should learn the loop doesn't support
// entity subscriptions rather than have the update silently apply
// to scope="".
func TestLoopUpdateEntitySubscriptions_RejectsLoopWithoutFocusTag(t *testing.T) {
	t.Parallel()
	rig := newCurateTestRig(t)

	// Hand-seed a definition with no focus_tag in Metadata.
	bareSpec := looppkg.Spec{
		Name:         "tagless",
		Enabled:      true,
		Task:         "no entity context",
		Operation:    looppkg.OperationService,
		SleepMin:     time.Hour,
		SleepMax:     time.Hour,
		SleepDefault: time.Hour,
	}
	if err := rig.defRegistry.Upsert(bareSpec, time.Now()); err != nil {
		t.Fatalf("seed bare definition: %v", err)
	}

	tool := rig.reg.Get("loop_update_entity_subscriptions")
	if tool == nil {
		t.Fatal("loop_update_entity_subscriptions not registered")
	}
	_, err := tool.Handler(context.Background(), map[string]any{
		"name": "tagless",
		"add":  []any{map[string]any{"entity_id": "sensor.foo"}},
	})
	if err == nil {
		t.Fatal("expected error for loop without focus_tag")
	}
	if !strings.Contains(err.Error(), "focus_tag") {
		t.Errorf("error %q should mention focus_tag", err)
	}
}

// TestLoopUpdateEntitySubscriptions_RequiresAddOrRemove guards against
// a no-op call where the model passed an empty change-set.
func TestLoopUpdateEntitySubscriptions_RequiresAddOrRemove(t *testing.T) {
	t.Parallel()
	rig := newCurateTestRig(t)

	if _, err := rig.tool.Handler(context.Background(), map[string]any{
		"name":    "loop_one",
		"intent":  "x",
		"cadence": "hourly",
		"output": map[string]any{
			"mode":     "journal",
			"document": "kb:none.md",
		},
		"entities": []any{map[string]any{"entity_id": "sensor.x"}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tool := rig.reg.Get("loop_update_entity_subscriptions")
	if tool == nil {
		t.Fatal("loop_update_entity_subscriptions not registered")
	}
	_, err := tool.Handler(context.Background(), map[string]any{"name": "loop_one"})
	if err == nil {
		t.Fatal("expected error for empty change-set")
	}
	if !strings.Contains(err.Error(), "add or remove") {
		t.Errorf("error %q should mention add or remove", err)
	}
}

// TestLoopUpdateEntitySubscriptions_AddErrorsScopedCorrectly guards
// against the parseCurateEntities → parseEntityList rename: validation
// errors raised while parsing the `add` parameter must refer to the
// `add` field, not the curate-side `entities` field name.
func TestLoopUpdateEntitySubscriptions_AddErrorsScopedCorrectly(t *testing.T) {
	t.Parallel()
	rig := newCurateTestRig(t)

	if _, err := rig.tool.Handler(context.Background(), map[string]any{
		"name":    "scope_test",
		"intent":  "x",
		"cadence": "hourly",
		"output": map[string]any{
			"mode":     "journal",
			"document": "kb:scope.md",
		},
		"entities": []any{map[string]any{"entity_id": "sensor.seed"}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tool := rig.reg.Get("loop_update_entity_subscriptions")
	if tool == nil {
		t.Fatal("loop_update_entity_subscriptions not registered")
	}
	// Trigger a missing-entity_id error inside add[0].
	_, err := tool.Handler(context.Background(), map[string]any{
		"name": "scope_test",
		"add":  []any{map[string]any{}},
	})
	if err == nil {
		t.Fatal("expected validation error from add[0]")
	}
	if !strings.Contains(err.Error(), "add[") {
		t.Errorf("error %q should be scoped to add[...] not entities[...]", err)
	}
	if strings.Contains(err.Error(), "entities[") {
		t.Errorf("error %q leaks the curate-side field name into the update tool's error path", err)
	}
}
