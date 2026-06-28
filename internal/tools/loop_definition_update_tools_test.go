package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// seedOverlayUpdateTarget creates a mutable overlay definition via
// loop_definition_set so the update tests have something editable (the
// config-seeded "metacog_like" is immutable).
func seedOverlayUpdateTarget(t *testing.T, deps *testLoopDefinitionDeps) {
	t.Helper()
	spec := looppkg.Spec{
		Name:       "update_target",
		Enabled:    true,
		Task:       "original task",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
		Profile: router.LoopProfile{
			Mission:      "background",
			Instructions: "old instructions",
		},
		SleepMin:     5 * time.Minute,
		SleepMax:     time.Hour,
		SleepDefault: 15 * time.Minute,
	}
	argsJSON, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		t.Fatalf("marshal set args: %v", err)
	}
	if _, err := deps.reg.Execute(context.Background(), "loop_definition_set", string(argsJSON)); err != nil {
		t.Fatalf("seed loop_definition_set: %v", err)
	}
}

// updateTargetExec runs loop_definition_update with the editable fields passed
// as flat top-level params alongside name (the *_update tool convention).
func updateTargetExec(t *testing.T, deps *testLoopDefinitionDeps, changes map[string]any) (string, error) {
	t.Helper()
	args := map[string]any{"name": "update_target"}
	for k, v := range changes {
		args[k] = v
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal update args: %v", err)
	}
	return deps.reg.Execute(context.Background(), "loop_definition_update", string(argsJSON))
}

func TestLoopDefinitionUpdate_MergesAndPreserves(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)
	seedOverlayUpdateTarget(t, deps)

	out, err := updateTargetExec(t, deps, map[string]any{
		"supervisor":              true,
		"supervisor_prob":         0.25,
		"supervisor_instructions": "review carefully",
		"instructions":            "be concise",
		"sleep_default":           "20m",
	})
	if err != nil {
		t.Fatalf("loop_definition_update: %v", err)
	}

	got := deps.persisted["update_target"]

	// Edited fields applied.
	if !got.Supervisor {
		t.Error("supervisor should be true after update")
	}
	if got.SupervisorProb != 0.25 {
		t.Errorf("supervisor_prob = %v, want 0.25", got.SupervisorProb)
	}
	if got.SupervisorProfile == nil || got.SupervisorProfile.Instructions != "review carefully" {
		t.Errorf("supervisor_profile.instructions = %#v, want auto-created with \"review carefully\"", got.SupervisorProfile)
	}
	if got.Profile.Instructions != "be concise" {
		t.Errorf("profile.instructions = %q, want \"be concise\"", got.Profile.Instructions)
	}
	if got.SleepDefault != 20*time.Minute {
		t.Errorf("sleep_default = %v, want 20m", got.SleepDefault)
	}

	// Untouched fields preserved exactly by the round-trip.
	if got.Task != "original task" {
		t.Errorf("task = %q, want preserved \"original task\"", got.Task)
	}
	if got.Profile.Mission != "background" {
		t.Errorf("profile.mission = %q, want preserved \"background\"", got.Profile.Mission)
	}
	if got.SleepMin != 5*time.Minute || got.SleepMax != time.Hour {
		t.Errorf("sleep envelope = [%v,%v], want preserved [5m,1h]", got.SleepMin, got.SleepMax)
	}

	// Result envelope reports what changed.
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if env["status"] != "ok" {
		t.Errorf("status = %v, want ok", env["status"])
	}
	fields, _ := env["updated_fields"].([]any)
	if len(fields) != 5 {
		t.Errorf("updated_fields = %v, want 5 entries", env["updated_fields"])
	}
}

func TestLoopDefinitionUpdate_UnknownFieldTeaches(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)
	seedOverlayUpdateTarget(t, deps)

	_, err := updateTargetExec(t, deps, map[string]any{"superviser": true}) // typo
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "editable fields") {
		t.Errorf("error = %q, want it to list the editable fields", err)
	}
}

func TestLoopDefinitionUpdate_RedirectsUneditableFields(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)
	seedOverlayUpdateTarget(t, deps)

	_, err := updateTargetExec(t, deps, map[string]any{"parent_name": "travel"})
	if err == nil || !strings.Contains(err.Error(), "loop_reparent") {
		t.Errorf("error = %v, want a redirect to loop_reparent for parent_name", err)
	}

	_, err = updateTargetExec(t, deps, map[string]any{"tags": []string{"home"}})
	if err == nil || !strings.Contains(err.Error(), "loop_definition_set") {
		t.Errorf("error = %v, want a redirect to loop_definition_set for list fields", err)
	}

	// enabled is a runtime-lifecycle flip, not an editable spec field: it can
	// stop a running loop mid-call and is ignored when a policy overlay exists.
	_, err = updateTargetExec(t, deps, map[string]any{"enabled": false})
	if err == nil || !strings.Contains(err.Error(), "loop_definition_set_policy") {
		t.Errorf("error = %v, want a redirect to loop_definition_set_policy for enabled", err)
	}
}

func TestLoopDefinitionUpdate_ConfigOwnedImmutable(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)

	argsJSON, _ := json.Marshal(map[string]any{
		"name":       "metacog_like", // config-seeded, immutable
		"supervisor": true,
	})
	_, err := deps.reg.Execute(context.Background(), "loop_definition_update", string(argsJSON))
	var immErr *looppkg.ImmutableDefinitionError
	if !errors.As(err, &immErr) {
		t.Fatalf("err = %v, want ImmutableDefinitionError for a config-owned definition", err)
	}
}

func TestLoopDefinitionUpdate_UnknownName(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)

	argsJSON, _ := json.Marshal(map[string]any{
		"name":       "does_not_exist",
		"supervisor": true,
	})
	_, err := deps.reg.Execute(context.Background(), "loop_definition_update", string(argsJSON))
	var unknownErr *looppkg.UnknownDefinitionError
	if !errors.As(err, &unknownErr) {
		t.Fatalf("err = %v, want UnknownDefinitionError", err)
	}
}

func TestLoopDefinitionUpdate_NoFieldsRejected(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)
	seedOverlayUpdateTarget(t, deps)

	argsJSON, _ := json.Marshal(map[string]any{"name": "update_target"})
	_, err := deps.reg.Execute(context.Background(), "loop_definition_update", string(argsJSON))
	if err == nil || !strings.Contains(err.Error(), "at least one editable field") {
		t.Errorf("err = %v, want a no-fields rejection", err)
	}
}

func TestLoopDefinitionUpdate_SkipsContentResolve(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)
	// #1068: the merged spec carries outputs[].ref and the field values are
	// literals — the tool must opt out of prefix-to-content resolution.
	tool := deps.reg.Get("loop_definition_update")
	if tool == nil {
		t.Fatal("loop_definition_update not registered")
	}
	if !tool.SkipContentResolve {
		t.Error("loop_definition_update must set SkipContentResolve to keep field values literal")
	}
}
