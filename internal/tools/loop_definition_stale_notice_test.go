package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// These tests pin the running-loop contracts of the definition-write
// surfaces, which split into two tiers. Relaunch tier (loop_definition_set,
// thane_loop_create replace, loop_definition_launch's short-circuit): a
// running loop keeps its launched-time config until a full relaunch
// (ReconcileDefinition deliberately no-ops on a live loop), so those results
// must carry the stale-config notice. Conforming tier (loop_definition_update):
// scalar retunes apply live via QueueRetune and the result confirms it
// instead. The notice resolves liveness through runningLoopByName, which
// falls back to the intent-tool deps' live registry — the wiring used here.
// The gate for both tiers is the prior instance SURVIVING the write: a loop
// spawned by the commit's own reconcile runs the just-written spec and must
// not be called stale (nor retuned twice).

// noticeHarness extends the shared loop-definition harness with a live loop
// registry so the running-loop notice path is reachable.
func noticeHarness(t *testing.T) (*testLoopDefinitionDeps, *looppkg.Registry) {
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
	return deps, live
}

func registerRunningLoop(t *testing.T, live *looppkg.Registry, name string) {
	t.Helper()
	l, err := looppkg.New(looppkg.Config{
		Name:      name,
		Operation: looppkg.OperationService,
		Task:      "do " + name,
	}, looppkg.Deps{Runner: noopLoopRunner{}})
	if err != nil {
		t.Fatalf("New(%s): %v", name, err)
	}
	if err := live.Register(l); err != nil {
		t.Fatalf("Register(%s): %v", name, err)
	}
}

func execLoopDefinitionSet(t *testing.T, deps *testLoopDefinitionDeps, spec looppkg.Spec) map[string]any {
	t.Helper()
	argsJSON, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		t.Fatalf("marshal set args: %v", err)
	}
	out, err := deps.reg.Execute(context.Background(), "loop_definition_set", string(argsJSON))
	if err != nil {
		t.Fatalf("loop_definition_set: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return env
}

func TestLoopDefinitionSetNoticeWhenLoopRunning(t *testing.T) {
	deps, live := noticeHarness(t)
	registerRunningLoop(t, live, "update_target")

	env := execLoopDefinitionSet(t, deps, looppkg.Spec{
		Name:       "update_target",
		Enabled:    true,
		Task:       "replacement task",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
	})
	notice, _ := env["notice"].(string)
	if !strings.Contains(notice, "currently running") || !strings.Contains(notice, "launched-time config") {
		t.Errorf("notice = %q, want the running-loop stale-config contract", notice)
	}
	// The write itself still lands — only the live instance is stale.
	if got := deps.persisted["update_target"].Task; got != "replacement task" {
		t.Errorf("persisted task = %q, want the replacement", got)
	}
}

// TestLoopDefinitionSetNoticeMatchesLiveOperation pins the notice's recipe to
// the SURVIVING instance's operation: set can rewrite operation while the
// running loop keeps its old shape, and relaunch guidance for a live service
// loop must not carry the container-children wording of the drifted spec.
func TestLoopDefinitionSetNoticeMatchesLiveOperation(t *testing.T) {
	deps, live := noticeHarness(t)
	registerRunningLoop(t, live, "update_target") // live SERVICE loop

	env := execLoopDefinitionSet(t, deps, looppkg.Spec{
		Name:      "update_target",
		Enabled:   true,
		Operation: looppkg.OperationContainer, // drifted spec
	})
	notice, _ := env["notice"].(string)
	if !strings.Contains(notice, "stop_loop then loop_definition_launch") {
		t.Errorf("notice = %q, want the service-loop relaunch recipe (live instance is a service)", notice)
	}
	if strings.Contains(notice, "children") {
		t.Errorf("notice = %q, carries container guidance for a live service loop", notice)
	}
}

func TestLoopDefinitionSetNoNoticeWhenLoopNotRunning(t *testing.T) {
	deps, _ := noticeHarness(t)

	env := execLoopDefinitionSet(t, deps, looppkg.Spec{
		Name:       "update_target",
		Enabled:    true,
		Task:       "fresh task",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
	})
	if notice, present := env["notice"]; present {
		t.Errorf("notice = %v on a non-running loop, want absent", notice)
	}
}

// TestLoopDefinitionUpdateAppliesLiveRetune pins the conformance contract
// (#1153): editing a running loop's scalar fields reaches the live instance
// via QueueRetune — no relaunch — and the result says so. The harness loop is
// registered but not started, so the engine promotes inline and the live
// config is observable immediately.
func TestLoopDefinitionUpdateAppliesLiveRetune(t *testing.T) {
	deps, live := noticeHarness(t)
	seedOverlayUpdateTarget(t, deps)
	registerRunningLoop(t, live, "update_target")

	out, err := updateTargetExec(t, deps, map[string]any{"task": "tweaked task"})
	if err != nil {
		t.Fatalf("loop_definition_update: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if env["retune"] != "applied" {
		t.Errorf("retune = %v, want applied", env["retune"])
	}
	notice, _ := env["notice"].(string)
	if !strings.Contains(notice, "applied live") {
		t.Errorf("notice = %q, want the applied-live confirmation", notice)
	}
	// The live loop's config conformed to the stored spec.
	running := live.GetByName("update_target")
	if running == nil {
		t.Fatal("live loop missing")
	}
	st := running.Status()
	if st.Config.Task != "tweaked task" {
		t.Errorf("live task = %q, want the retuned %q", st.Config.Task, "tweaked task")
	}
	if st.PendingRetune {
		t.Error("pending_retune = true after promote, want false")
	}
}

func TestLoopDefinitionUpdateNoNoticeWhenLoopNotRunning(t *testing.T) {
	deps, _ := noticeHarness(t)
	seedOverlayUpdateTarget(t, deps)

	out, err := updateTargetExec(t, deps, map[string]any{"task": "tweaked task"})
	if err != nil {
		t.Fatalf("loop_definition_update: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if notice, present := env["notice"]; present {
		t.Errorf("notice = %v on a non-running loop, want absent", notice)
	}
}

// TestLoopDefinitionSetNoNoticeWhenCommitSpawnsTheLoop pins the false-positive
// guard: in production, commitLoopDefinition's reconcile SPAWNS an absent
// active definition, so after a fresh create the loop is live — but it is
// running the just-written spec, not a stale one, and must not get the notice.
func TestLoopDefinitionSetNoNoticeWhenCommitSpawnsTheLoop(t *testing.T) {
	defs, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	live := looppkg.NewRegistry()
	reg := NewEmptyRegistry()
	reg.ConfigureLoopDefinitionTools(LoopDefinitionToolDeps{
		Registry: defs,
		CommitSpec: func(_ context.Context, spec looppkg.Spec, updatedAt time.Time) error {
			if err := defs.Upsert(spec, updatedAt); err != nil {
				return err
			}
			// Mirror ReconcileDefinition's spawn branch: an absent active
			// durable definition starts as part of the commit.
			if live.GetByName(spec.Name) == nil {
				l, err := looppkg.New(looppkg.Config{
					Name:      spec.Name,
					Operation: spec.Operation,
					Task:      spec.Task,
				}, looppkg.Deps{Runner: noopLoopRunner{}})
				if err != nil {
					return err
				}
				return live.Register(l)
			}
			return nil
		},
	})
	reg.ConfigureLoopIntentTools(LoopIntentToolDeps{
		Registry:   defs,
		CommitSpec: upsertCommitSpec(defs),
		LaunchDefinition: func(context.Context, string, looppkg.Launch) (looppkg.LaunchResult, error) {
			return looppkg.LaunchResult{}, nil
		},
		LiveRegistry: live,
	})

	argsJSON, err := json.Marshal(map[string]any{"spec": looppkg.Spec{
		Name:       "fresh_loop",
		Enabled:    true,
		Task:       "brand new task",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
	}})
	if err != nil {
		t.Fatalf("marshal set args: %v", err)
	}
	out, err := reg.Execute(context.Background(), "loop_definition_set", string(argsJSON))
	if err != nil {
		t.Fatalf("loop_definition_set: %v", err)
	}
	if live.GetByName("fresh_loop") == nil {
		t.Fatal("test harness broken: commit should have spawned the loop")
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if notice, present := env["notice"]; present {
		t.Errorf("notice = %v on a commit-spawned loop, want absent (it runs the just-written spec)", notice)
	}
}

// TestLoopDefinitionLaunchFlagsReusedRunningLoop pins the launch-side honesty
// flag: LaunchDefinition short-circuits to an already-running durable loop, so
// the result must mark reused_running_loop — otherwise a stop_loop-then-launch
// relaunch that raced a slow drain is indistinguishable from a fresh start.
func TestLoopDefinitionLaunchFlagsReusedRunningLoop(t *testing.T) {
	defs, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	if err := defs.Upsert(looppkg.Spec{
		Name:      "trip",
		Enabled:   true,
		Task:      "do trip",
		Operation: looppkg.OperationService,
	}, time.Now().UTC()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	live := looppkg.NewRegistry()
	reg := NewEmptyRegistry()
	reg.ConfigureLoopDefinitionTools(LoopDefinitionToolDeps{
		Registry:   defs,
		CommitSpec: upsertCommitSpec(defs),
		LaunchDefinition: func(_ context.Context, name string, _ looppkg.Launch) (looppkg.LaunchResult, error) {
			// Mirror LaunchDefinition's running-durable short-circuit.
			if existing := live.GetByName(name); existing != nil {
				return looppkg.LaunchResult{LoopID: existing.ID(), Detached: true}, nil
			}
			return looppkg.LaunchResult{LoopID: "fresh-loop-id", Detached: true}, nil
		},
	})
	reg.ConfigureLoopIntentTools(LoopIntentToolDeps{
		Registry:   defs,
		CommitSpec: upsertCommitSpec(defs),
		LaunchDefinition: func(context.Context, string, looppkg.Launch) (looppkg.LaunchResult, error) {
			return looppkg.LaunchResult{}, nil
		},
		LiveRegistry: live,
	})

	launch := func() map[string]any {
		out, err := reg.Execute(context.Background(), "loop_definition_launch", `{"name":"trip"}`)
		if err != nil {
			t.Fatalf("loop_definition_launch: %v", err)
		}
		var env map[string]any
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		return env
	}

	// Fresh start: no live loop, no reuse flag.
	env := launch()
	if flag, present := env["reused_running_loop"]; present {
		t.Errorf("fresh launch: reused_running_loop = %v, want absent", flag)
	}

	// Running loop: launch short-circuits and must say so.
	registerRunningLoop(t, live, "trip")
	env = launch()
	if env["reused_running_loop"] != true {
		t.Errorf("running launch: reused_running_loop = %v, want true", env["reused_running_loop"])
	}
	notice, _ := env["notice"].(string)
	if !strings.Contains(notice, "already running") || !strings.Contains(notice, "launched-time config") {
		t.Errorf("notice = %q, want the reused-loop contract", notice)
	}
}
