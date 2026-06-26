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

// seedOverlayPatchTarget creates a mutable overlay definition via
// loop_definition_set so the patch tests have something patchable (the
// config-seeded "metacog_like" is immutable).
func seedOverlayPatchTarget(t *testing.T, deps *testLoopDefinitionDeps) {
	t.Helper()
	spec := looppkg.Spec{
		Name:       "patch_target",
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

func patchTargetExec(t *testing.T, deps *testLoopDefinitionDeps, patch map[string]any) (string, error) {
	t.Helper()
	argsJSON, err := json.Marshal(map[string]any{"name": "patch_target", "patch": patch})
	if err != nil {
		t.Fatalf("marshal patch args: %v", err)
	}
	return deps.reg.Execute(context.Background(), "loop_definition_patch", string(argsJSON))
}

func TestLoopDefinitionPatch_MergesAndPreserves(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)
	seedOverlayPatchTarget(t, deps)

	out, err := patchTargetExec(t, deps, map[string]any{
		"supervisor":              true,
		"supervisor_prob":         0.25,
		"supervisor_instructions": "review carefully",
		"instructions":            "be concise",
		"sleep_default":           "20m",
	})
	if err != nil {
		t.Fatalf("loop_definition_patch: %v", err)
	}

	got := deps.persisted["patch_target"]

	// Patched fields applied.
	if !got.Supervisor {
		t.Error("supervisor should be true after patch")
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
	fields, _ := env["patched_fields"].([]any)
	if len(fields) != 5 {
		t.Errorf("patched_fields = %v, want 5 entries", env["patched_fields"])
	}
}

func TestLoopDefinitionPatch_UnknownFieldTeaches(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)
	seedOverlayPatchTarget(t, deps)

	_, err := patchTargetExec(t, deps, map[string]any{"superviser": true}) // typo
	if err == nil {
		t.Fatal("expected error for unknown patch field")
	}
	if !strings.Contains(err.Error(), "patchable fields") {
		t.Errorf("error = %q, want it to list the patchable fields", err)
	}
}

func TestLoopDefinitionPatch_RedirectsStructuralFields(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)
	seedOverlayPatchTarget(t, deps)

	_, err := patchTargetExec(t, deps, map[string]any{"parent_name": "travel"})
	if err == nil {
		t.Fatal("expected error for parent_name patch")
	}
	if !strings.Contains(err.Error(), "loop_reparent") {
		t.Errorf("error = %q, want a redirect to loop_reparent", err)
	}

	_, err = patchTargetExec(t, deps, map[string]any{"tags": []string{"home"}})
	if err == nil || !strings.Contains(err.Error(), "loop_definition_set") {
		t.Errorf("error = %v, want a redirect to loop_definition_set for list fields", err)
	}

	// enabled is a runtime-lifecycle flip, not a patchable spec field: it can
	// stop a running loop mid-call and is ignored when a policy overlay exists.
	_, err = patchTargetExec(t, deps, map[string]any{"enabled": false})
	if err == nil || !strings.Contains(err.Error(), "loop_definition_set_policy") {
		t.Errorf("error = %v, want a redirect to loop_definition_set_policy for enabled", err)
	}
}

func TestLoopDefinitionPatch_ConfigOwnedImmutable(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)

	argsJSON, _ := json.Marshal(map[string]any{
		"name":  "metacog_like", // config-seeded, immutable
		"patch": map[string]any{"supervisor": true},
	})
	_, err := deps.reg.Execute(context.Background(), "loop_definition_patch", string(argsJSON))
	var immErr *looppkg.ImmutableDefinitionError
	if !errors.As(err, &immErr) {
		t.Fatalf("err = %v, want ImmutableDefinitionError for a config-owned definition", err)
	}
}

func TestLoopDefinitionPatch_UnknownName(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)

	argsJSON, _ := json.Marshal(map[string]any{
		"name":  "does_not_exist",
		"patch": map[string]any{"supervisor": true},
	})
	_, err := deps.reg.Execute(context.Background(), "loop_definition_patch", string(argsJSON))
	var unknownErr *looppkg.UnknownDefinitionError
	if !errors.As(err, &unknownErr) {
		t.Fatalf("err = %v, want UnknownDefinitionError", err)
	}
}

func TestLoopDefinitionPatch_EmptyPatchRejected(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)
	seedOverlayPatchTarget(t, deps)

	_, err := patchTargetExec(t, deps, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "at least one field") {
		t.Errorf("err = %v, want an empty-patch rejection", err)
	}
}

func TestLoopDefinitionPatch_SkipsContentResolve(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)
	// #1068: the merged spec carries outputs[].ref and the patch values are
	// literals — the tool must opt out of prefix-to-content resolution.
	tool := deps.reg.Get("loop_definition_patch")
	if tool == nil {
		t.Fatal("loop_definition_patch not registered")
	}
	if !tool.SkipContentResolve {
		t.Error("loop_definition_patch must set SkipContentResolve to keep spec/patch values literal")
	}
}
