package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// removalHarness is the delete-path rig: the shared definition harness
// plus a live registry, with an overlay definition that declares
// outputs and a running instance registered under the same name — the
// exact shape of the prod incident this file's tests exist to prevent
// recurring.
func removalHarness(t *testing.T, reconcileStops bool) (*testLoopDefinitionDeps, *looppkg.Registry) {
	t.Helper()
	deps := newTestLoopDefinitionDeps(t)
	live := looppkg.NewRegistry()
	deps.reg.ConfigureLoopIntentTools(LoopIntentToolDeps{
		Registry:   deps.defs,
		CommitSpec: upsertCommitSpec(deps.defs),
		LaunchDefinition: func(context.Context, string, looppkg.Launch) (looppkg.LaunchResult, error) {
			return looppkg.LaunchResult{}, nil
		},
		LiveRegistry: live,
	})
	if reconcileStops {
		// Convergence stand-in: the real reconciler stops a loop whose
		// definition vanished; here that is a deregister.
		deps.reg.reconcileLoopDefinition = func(_ context.Context, name string) error {
			if l := live.GetByName(name); l != nil {
				live.Deregister(l.ID())
			}
			return nil
		}
	}

	if err := deps.defs.Upsert(looppkg.Spec{
		Name:       "office_watch",
		Enabled:    true,
		Task:       "Watch the office.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
		Outputs: []looppkg.OutputSpec{
			{Name: "office_watch", Type: looppkg.OutputTypeMaintainedDocument, Ref: "projects:home/office.md"},
			{Name: "office_watch_notes", Type: looppkg.OutputTypeWorkingNotes, Ref: "projects:home/office-notes.md"},
		},
	}, time.Now()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	registerRunningLoop(t, live, "office_watch")
	return deps, live
}

func runDelete(t *testing.T, deps *testLoopDefinitionDeps, name string) map[string]any {
	t.Helper()
	out, err := deps.reg.Get("loop_definition_delete").Handler(context.Background(), map[string]any{"name": name})
	if err != nil {
		t.Fatalf("loop_definition_delete: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return result
}

// TestLoopDefinitionDeleteReportsStoppedInstance pins the result's live
// half: the delete stopped a running instance and SAYS so, as verified
// fact (the registry was re-checked after reconcile), with the loop's
// documents listed as deliberately left behind. A result silent about
// the live layer is how a phantom kill goes unnoticed.
func TestLoopDefinitionDeleteReportsStoppedInstance(t *testing.T) {
	deps, live := removalHarness(t, true)
	result := runDelete(t, deps, "office_watch")

	stopped, ok := result["running_loop_stopped"].(map[string]any)
	if !ok {
		t.Fatalf("result missing running_loop_stopped: %v", result)
	}
	if stopped["loop_id"] == "" || stopped["loop_id"] == nil {
		t.Errorf("stopped instance should carry its loop_id: %v", stopped)
	}
	if live.GetByName("office_watch") != nil {
		t.Error("live loop survived the delete in the converged harness")
	}
	refs, ok := result["declared_outputs_left_in_place"].([]any)
	if !ok || len(refs) != 2 {
		t.Fatalf("declared_outputs_left_in_place = %v, want both refs", result["declared_outputs_left_in_place"])
	}
	note, _ := result["outputs_note"].(string)
	if !strings.Contains(note, "NOT deleted") {
		t.Errorf("outputs_note should say the documents were kept: %q", note)
	}
}

// TestLoopDefinitionDeleteReportsSurvivor pins the honest-failure path:
// if reconcile does not converge the instance away, the result says the
// loop is STILL LIVE and teaches the recovery, instead of reporting a
// clean delete that leaves a haunting.
func TestLoopDefinitionDeleteReportsSurvivor(t *testing.T) {
	deps, live := removalHarness(t, false)
	result := runDelete(t, deps, "office_watch")

	survivor, ok := result["running_loop_still_live"].(map[string]any)
	if !ok {
		t.Fatalf("result missing running_loop_still_live: %v", result)
	}
	note, _ := survivor["note"].(string)
	if !strings.Contains(note, "stop_loop") || !strings.Contains(note, "loop_status") {
		t.Errorf("survivor note should teach the recovery: %q", note)
	}
	if live.GetByName("office_watch") == nil {
		t.Fatal("harness invariant: the no-converge rig should leave the loop live")
	}
}

// TestLoopDefinitionDeleteReportsNoRunningLoop pins the quiet case: a
// definition with no live instance says so, so absence reads as
// verified rather than unmentioned.
func TestLoopDefinitionDeleteReportsNoRunningLoop(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)
	live := looppkg.NewRegistry()
	deps.reg.ConfigureLoopIntentTools(LoopIntentToolDeps{
		Registry:   deps.defs,
		CommitSpec: upsertCommitSpec(deps.defs),
		LaunchDefinition: func(context.Context, string, looppkg.Launch) (looppkg.LaunchResult, error) {
			return looppkg.LaunchResult{}, nil
		},
		LiveRegistry: live,
	})
	if err := deps.defs.Upsert(looppkg.Spec{
		Name:       "quiet_watch",
		Enabled:    true,
		Task:       "Watch quietly.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
	}, time.Now()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	result := runDelete(t, deps, "quiet_watch")
	if result["no_running_loop"] != true {
		t.Errorf("result should report no_running_loop, got %v", result)
	}
	if _, present := result["declared_outputs_left_in_place"]; present {
		t.Errorf("no outputs declared, none should be reported: %v", result)
	}
}

// TestStopLoopFlagsActiveDefinition pins the resurrection trap: a stop
// of a loop whose durable definition is still active is temporary — the
// reconciler relaunches it — and the result must say so and name the
// durable doors. Without the stamp, "stop the loop" produces a loop
// that comes back later, unexplained.
func TestStopLoopFlagsActiveDefinition(t *testing.T) {
	deps := newTestLoopRuntimeDeps(t)
	defs, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	if err := defs.Upsert(looppkg.Spec{
		Name:       "battery_watch",
		Enabled:    true,
		Task:       "Watch batteries.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
	}, time.Now()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	deps.reg.ConfigureLoopDefinitionTools(LoopDefinitionToolDeps{
		Registry: defs,
		View: func() *looppkg.DefinitionRegistryView {
			return looppkg.BuildDefinitionRegistryView(defs.Snapshot(), nil)
		},
	})

	out, err := deps.reg.Get("stop_loop").Handler(context.Background(), map[string]any{"name": "battery_watch"})
	if err != nil {
		t.Fatalf("stop_loop: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["definition_active"] != true {
		t.Fatalf("stop of an active durable definition must be flagged: %v", result)
	}
	warn, _ := result["will_relaunch"].(string)
	for _, want := range []string{"reconciler", "paused", "loop_definition_delete"} {
		if !strings.Contains(warn, want) {
			t.Errorf("will_relaunch should teach %q: %q", want, warn)
		}
	}
}

// TestStopLoopWithoutDefinitionHasNoFlag pins the boundary: an ad hoc
// loop with no stored definition stops for good, and the result carries
// no resurrection warning to cry wolf about.
func TestStopLoopWithoutDefinitionHasNoFlag(t *testing.T) {
	deps := newTestLoopRuntimeDeps(t)
	out, err := deps.reg.Get("stop_loop").Handler(context.Background(), map[string]any{"name": "battery_watch"})
	if err != nil {
		t.Fatalf("stop_loop: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := result["definition_active"]; present {
		t.Errorf("no definition exists; no flag should be raised: %v", result)
	}
}

// TestLoopDefinitionDeleteUnverifiedWithoutLiveRegistry pins the honest
// degradation: a runtime with no live registry cannot check whether an
// instance existed, and must say the check could not run instead of
// dressing an unchecked absence up as a verified no_running_loop.
func TestLoopDefinitionDeleteUnverifiedWithoutLiveRegistry(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)
	if err := deps.defs.Upsert(looppkg.Spec{
		Name:       "unwired_watch",
		Enabled:    true,
		Task:       "Watch without a live registry.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
	}, time.Now()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	result := runDelete(t, deps, "unwired_watch")
	if result["live_outcome_unverified"] != true {
		t.Fatalf("nil live registry must report the live outcome as unverified: %v", result)
	}
	for _, forbidden := range []string{"no_running_loop", "running_loop_stopped", "running_loop_still_live"} {
		if _, present := result[forbidden]; present {
			t.Errorf("%s claims a verification that never ran: %v", forbidden, result)
		}
	}
}

// TestStopLoopTransientDefinitionHasNoFlag pins the durable gate on the
// resurrection warning: the reconciler never relaunches a transient
// operation (it launches on demand), so an active background_task
// definition must not trigger the will_relaunch wolf-cry.
func TestStopLoopTransientDefinitionHasNoFlag(t *testing.T) {
	deps := newTestLoopRuntimeDeps(t)
	defs, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	if err := defs.Upsert(looppkg.Spec{
		Name:       "mqtt_bridge",
		Enabled:    true,
		Task:       "Bridge MQTT events.",
		Operation:  looppkg.OperationBackgroundTask,
		Completion: looppkg.CompletionChannel,
	}, time.Now()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	deps.reg.ConfigureLoopDefinitionTools(LoopDefinitionToolDeps{
		Registry: defs,
		View: func() *looppkg.DefinitionRegistryView {
			return looppkg.BuildDefinitionRegistryView(defs.Snapshot(), nil)
		},
	})

	out, err := deps.reg.Get("stop_loop").Handler(context.Background(), map[string]any{"name": "mqtt_bridge"})
	if err != nil {
		t.Fatalf("stop_loop: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := result["definition_active"]; present {
		t.Errorf("transient definitions are never resurrected; no flag should be raised: %v", result)
	}
}
