package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestUpdateEntitySubscriptions_AddRemove exercises the happy path:
// a loop's spec.Subscriptions is rewritten in place — removes drop
// their entries, adds append new ones, and the resulting list is
// what the spec carries after the call.
func TestUpdateEntitySubscriptions_AddRemove(t *testing.T) {
	t.Parallel()
	rig := newCurateTestRig(t)

	if _, err := rig.tool.Handler(context.Background(), map[string]any{
		"name":      "watcher",
		"intent":    "watch things",
		"sleep_min": "54m",
		"sleep_max": "66m",
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

	updateTool := rig.reg.Get("update_entity_subscriptions")
	if updateTool == nil {
		t.Fatal("update_entity_subscriptions not registered")
	}

	result, err := updateTool.Handler(context.Background(), map[string]any{
		"name": "watcher",
		"add": []any{
			map[string]any{
				"entity_id": "sensor.new",
				"history":   []any{600},
				"include": map[string]any{
					"area":        true,
					"labels":      true,
					"description": true,
				},
			},
			map[string]any{"entity_id": "weather.home", "forecast": "hourly"},
		},
		"remove": []any{"sensor.old"},
	})
	if err != nil {
		t.Fatalf("update_entity_subscriptions: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status = %v, want ok", resp["status"])
	}
	if resp["added"] != float64(2) || resp["removed"] != float64(1) {
		t.Errorf("counts wrong: added=%v removed=%v", resp["added"], resp["removed"])
	}
	if resp["subscription_count"] != float64(2) {
		t.Errorf("subscription_count = %v, want 2", resp["subscription_count"])
	}

	spec := rig.findCurateSpec(t, "watcher")
	if len(spec.Subscriptions) != 2 {
		t.Fatalf("Subscriptions len = %d, want 2: %+v", len(spec.Subscriptions), spec.Subscriptions)
	}
	for _, sub := range spec.Subscriptions {
		if sub.EntityID == "sensor.old" {
			t.Errorf("sensor.old still present after remove")
		}
	}
	var seenNew, seenWeather bool
	for _, sub := range spec.Subscriptions {
		if sub.EntityID == "sensor.new" {
			seenNew = true
			if len(sub.History) != 1 || sub.History[0] != 600 {
				t.Errorf("sensor.new history = %v, want [600]", sub.History)
			}
			if sub.Include == nil || !sub.Include.Area || !sub.Include.Labels || !sub.Include.Description || sub.Include.Device {
				t.Errorf("sensor.new include = %#v, want area+labels+description", sub.Include)
			}
		}
		if sub.EntityID == "weather.home" {
			seenWeather = true
			if sub.Forecast != "hourly" {
				t.Errorf("weather.home forecast = %q, want hourly", sub.Forecast)
			}
		}
	}
	if !seenNew || !seenWeather {
		t.Errorf("missing expected entries; subs = %+v", spec.Subscriptions)
	}
}

// TestUpdateEntitySubscriptions_UnknownLoop covers the actionable-
// error path when the named loop isn't in the registry.
func TestUpdateEntitySubscriptions_UnknownLoop(t *testing.T) {
	t.Parallel()
	rig := newCurateTestRig(t)

	tool := rig.reg.Get("update_entity_subscriptions")
	if tool == nil {
		t.Fatal("update_entity_subscriptions not registered")
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
	// The error must teach the next move (docs/model-facing-tools.md §4):
	// point to the always-visible tool for own-context watches and to the
	// loop lister for targeting a real loop — the fork the model took wrong
	// when it passed a conversation id as the loop name.
	for _, want := range []string{"add_entity_subscription", "loop_definition_list"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("unknown-loop error %q should mention %q to teach the next move", err, want)
		}
	}
}

// TestUpdateEntitySubscriptions_AppliesToTaglessLoop covers the
// post-migration behavior: any overlay loop can carry subscriptions,
// not just curate-style loops. A hand-seeded service spec with no
// metadata still accepts add/remove via update_entity_subscriptions.
func TestUpdateEntitySubscriptions_AppliesToTaglessLoop(t *testing.T) {
	t.Parallel()
	rig := newCurateTestRig(t)

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

	tool := rig.reg.Get("update_entity_subscriptions")
	if tool == nil {
		t.Fatal("update_entity_subscriptions not registered")
	}
	if _, err := tool.Handler(context.Background(), map[string]any{
		"name": "tagless",
		"add":  []any{map[string]any{"entity_id": "sensor.foo"}},
	}); err != nil {
		t.Fatalf("update on tagless loop: %v", err)
	}

	spec := rig.findCurateSpec(t, "tagless")
	if len(spec.Subscriptions) != 1 || spec.Subscriptions[0].EntityID != "sensor.foo" {
		t.Errorf("Subscriptions = %+v, want one entry for sensor.foo", spec.Subscriptions)
	}
}

// TestUpdateEntitySubscriptions_RequiresAddOrRemove guards against
// a no-op call where the model passed an empty change-set.
func TestUpdateEntitySubscriptions_RequiresAddOrRemove(t *testing.T) {
	t.Parallel()
	rig := newCurateTestRig(t)

	if _, err := rig.tool.Handler(context.Background(), map[string]any{
		"name":      "loop_one",
		"intent":    "x",
		"sleep_min": "54m",
		"sleep_max": "66m",
		"output": map[string]any{
			"mode":     "journal",
			"document": "kb:none.md",
		},
		"entities": []any{map[string]any{"entity_id": "sensor.x"}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tool := rig.reg.Get("update_entity_subscriptions")
	if tool == nil {
		t.Fatal("update_entity_subscriptions not registered")
	}
	_, err := tool.Handler(context.Background(), map[string]any{"name": "loop_one"})
	if err == nil {
		t.Fatal("expected error for empty change-set")
	}
	if !strings.Contains(err.Error(), "add or remove") {
		t.Errorf("error %q should mention add or remove", err)
	}
}

// TestUpdateEntitySubscriptions_AddErrorsScopedCorrectly guards
// against the parseCurateEntities → parseEntityList rename: validation
// errors raised while parsing the `add` parameter must refer to the
// `add` field, not the curate-side `entities` field name.
func TestUpdateEntitySubscriptions_AddErrorsScopedCorrectly(t *testing.T) {
	t.Parallel()
	rig := newCurateTestRig(t)

	if _, err := rig.tool.Handler(context.Background(), map[string]any{
		"name":      "scope_test",
		"intent":    "x",
		"sleep_min": "54m",
		"sleep_max": "66m",
		"output": map[string]any{
			"mode":     "journal",
			"document": "kb:scope.md",
		},
		"entities": []any{map[string]any{"entity_id": "sensor.seed"}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tool := rig.reg.Get("update_entity_subscriptions")
	if tool == nil {
		t.Fatal("update_entity_subscriptions not registered")
	}
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
