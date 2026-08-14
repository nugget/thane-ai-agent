package loop

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// These tests pin the retune conformance contract (#1153): a running loop
// embodies its stored spec at every moment it is not mid-turn. QueueRetune
// promotes hot-swappable scalars at the engine's safe points — immediately
// while sleeping or waiting (without burning an iteration), at turn end when
// a turn is in flight — and re-clamps an in-flight sleep against an edited
// envelope, waking now if overdue.

// waitForCond polls cond until it returns true or the deadline passes.
func waitForCond(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// retuneSpec builds a valid service spec for QueueRetune with the given task
// and a uniform sleep envelope.
func retuneSpec(task string, sleep time.Duration, maxIter int) Spec {
	return Spec{
		Name:         "retune-target",
		Enabled:      true,
		Task:         task,
		Operation:    OperationService,
		Completion:   CompletionNone,
		SleepMin:     sleep,
		SleepMax:     sleep,
		SleepDefault: sleep,
		Jitter:       Float64Ptr(0),
		MaxIter:      maxIter,
	}
}

func TestQueueRetunePromotesDuringSleepWithoutIteration(t *testing.T) {
	t.Parallel()

	var count atomic.Int32
	l, err := New(Config{
		Name:         "retune-target",
		Task:         "old task",
		SleepMin:     time.Hour,
		SleepMax:     time.Hour,
		SleepDefault: time.Hour,
		Jitter:       Float64Ptr(0),
	}, Deps{Runner: &countingRunner{count: &count}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(l.Stop)

	if err := l.QueueRetune(retuneSpec("new task", time.Hour, 0)); err != nil {
		t.Fatalf("QueueRetune: %v", err)
	}

	waitForCond(t, 2*time.Second, "retune promoted during sleep", func() bool {
		s := l.Status()
		return s.Config.Task == "new task" && !s.PendingRetune
	})
	if got := count.Load(); got != 0 {
		t.Errorf("iterations burned by a sleeping-loop retune = %d, want 0", got)
	}
}

func TestQueueRetuneReclampsOverdueSleepAndWakes(t *testing.T) {
	t.Parallel()

	var count atomic.Int32
	l, err := New(Config{
		Name:         "retune-target",
		Task:         "old task",
		SleepMin:     time.Hour,
		SleepMax:     time.Hour,
		SleepDefault: time.Hour,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
	}, Deps{Runner: &countingRunner{count: &count}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(l.Stop)

	// Tighten the envelope far below the elapsed sleep: the re-clamped
	// deadline is overdue, so the loop wakes now and runs its iteration.
	if err := l.QueueRetune(retuneSpec("new task", time.Millisecond, 1)); err != nil {
		t.Fatalf("QueueRetune: %v", err)
	}

	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not wake from a re-clamped overdue sleep")
	}
	if got := count.Load(); got != 1 {
		t.Errorf("iterations after overdue-wake = %d, want 1", got)
	}
}

func TestQueueRetuneExtendsInFlightSleep(t *testing.T) {
	t.Parallel()

	var count atomic.Int32
	l, err := New(Config{
		Name:         "retune-target",
		Task:         "old task",
		SleepMin:     2 * time.Second,
		SleepMax:     2 * time.Second,
		SleepDefault: 2 * time.Second,
		Jitter:       Float64Ptr(0),
	}, Deps{Runner: &countingRunner{count: &count}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(l.Stop)

	if err := l.QueueRetune(retuneSpec("new task", time.Hour, 0)); err != nil {
		t.Fatalf("QueueRetune: %v", err)
	}
	waitForCond(t, 2*time.Second, "retune promoted", func() bool {
		return l.Status().Config.Task == "new task"
	})

	// The in-flight 2s sleep re-clamps into the 1h envelope: the scheduled
	// wake moves far out and no iteration fires.
	s := l.Status()
	if s.SleepUntil.Before(time.Now().Add(30 * time.Minute)) {
		t.Errorf("sleepUntil = %v, want re-clamped ~1h out", s.SleepUntil)
	}
	if got := count.Load(); got != 0 {
		t.Errorf("iterations after extending retune = %d, want 0", got)
	}
}

func TestQueueRetuneDefersMidTurnThenPromotes(t *testing.T) {
	t.Parallel()

	gate := make(chan struct{})
	l, err := New(Config{
		Name:         "retune-target",
		Task:         "old task",
		SleepMin:     5 * time.Millisecond,
		SleepMax:     5 * time.Millisecond,
		SleepDefault: 5 * time.Millisecond,
		Jitter:       Float64Ptr(0),
	}, Deps{Runner: &blockingRunner{gate: gate}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(l.Stop)

	waitForCond(t, 2*time.Second, "first turn in flight", func() bool {
		return l.Status().State == StateProcessing
	})

	if err := l.QueueRetune(retuneSpec("new task", 5*time.Millisecond, 0)); err != nil {
		t.Fatalf("QueueRetune: %v", err)
	}

	// The in-flight turn is never torn: while it runs, the retune stays
	// pending and the live config keeps the launch-time task.
	waitForCond(t, 2*time.Second, "pending retune visible mid-turn", func() bool {
		return l.Status().PendingRetune
	})
	if got := l.Status().Config.Task; got != "old task" {
		t.Errorf("mid-turn task = %q, want launch-time %q", got, "old task")
	}

	close(gate)

	// The moment the turn completes, the promotion lands.
	waitForCond(t, 2*time.Second, "retune promoted after turn end", func() bool {
		s := l.Status()
		return s.Config.Task == "new task" && !s.PendingRetune
	})
}

func TestQueueRetunePromotesWhileWaitingEventDriven(t *testing.T) {
	t.Parallel()

	var count atomic.Int32
	l, err := New(Config{
		Name:      "retune-target",
		Task:      "old task",
		Operation: OperationEventDriven,
	}, Deps{Runner: &countingRunner{count: &count}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(l.Stop)

	waitForCond(t, 2*time.Second, "event-driven loop waiting", func() bool {
		return l.Status().State == StateWaiting
	})

	spec := Spec{
		Name:       "retune-target",
		Enabled:    true,
		Task:       "new task",
		Operation:  OperationEventDriven,
		Completion: CompletionNone,
	}
	if err := l.QueueRetune(spec); err != nil {
		t.Fatalf("QueueRetune: %v", err)
	}

	// Promotion is not a wake: the config conforms without an iteration.
	waitForCond(t, 2*time.Second, "retune promoted while waiting", func() bool {
		s := l.Status()
		return s.Config.Task == "new task" && !s.PendingRetune
	})
	if got := count.Load(); got != 0 {
		t.Errorf("iterations burned by a waiting-loop retune = %d, want 0", got)
	}
}

func TestQueueRetuneUnstartedLoopPromotesInline(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name:         "retune-target",
		Task:         "old task",
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
		Jitter:       Float64Ptr(0),
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := l.QueueRetune(retuneSpec("new task", time.Minute, 0)); err != nil {
		t.Fatalf("QueueRetune: %v", err)
	}
	s := l.Status()
	if s.Config.Task != "new task" || s.PendingRetune {
		t.Errorf("unstarted loop: task=%q pending=%v, want inline promote", s.Config.Task, s.PendingRetune)
	}
}

// TestQueueRetunePromotesPromptMode pins the #1171 rollout path: flipping
// prompt_mode on a stored worker-loop definition reaches the live loop as
// a retune — the requestBase rebuild in promoteRetuneLocked carries it —
// so the trim needs no relaunch.
func TestQueueRetunePromotesPromptMode(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name:         "retune-target",
		Task:         "old task",
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
		Jitter:       Float64Ptr(0),
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	retuned := retuneSpec("old task", time.Minute, 0)
	retuned.PromptMode = agentctx.PromptModeTask
	if err := l.QueueRetune(retuned); err != nil {
		t.Fatalf("QueueRetune: %v", err)
	}

	req, err := l.prepareAgentTurnRequest(Request{}, "conv-1", false)
	if err != nil {
		t.Fatalf("prepareAgentTurnRequest: %v", err)
	}
	if req.PromptMode != agentctx.PromptModeTask {
		t.Errorf("PromptMode after retune = %q, want task", req.PromptMode)
	}
}

// TestQueueRetunePromptModeVersusLaunchOverride pins the shadowing rule:
// a launch-time prompt_mode override would otherwise outrank the retuned
// spec forever (prepareAgentTurnRequest prefers the override), so a
// retune that declares a mode clears the override — the same route the
// task override takes — while a retune silent on prompt_mode leaves the
// launch-time choice in place.
func TestQueueRetunePromptModeVersusLaunchOverride(t *testing.T) {
	t.Parallel()

	newOverriddenLoop := func(t *testing.T) *Loop {
		t.Helper()
		l, err := NewFromLaunch(Launch{
			Spec:       retuneSpec("old task", time.Minute, 0),
			PromptMode: agentctx.PromptModeFull,
		}, Deps{Runner: &noopRunner{}})
		if err != nil {
			t.Fatalf("NewFromLaunch: %v", err)
		}
		return l
	}

	t.Run("a declared retune mode clears the launch override", func(t *testing.T) {
		l := newOverriddenLoop(t)
		retuned := retuneSpec("old task", time.Minute, 0)
		retuned.PromptMode = agentctx.PromptModeTask
		if err := l.QueueRetune(retuned); err != nil {
			t.Fatalf("QueueRetune: %v", err)
		}
		req, err := l.prepareAgentTurnRequest(Request{}, "conv-1", false)
		if err != nil {
			t.Fatalf("prepareAgentTurnRequest: %v", err)
		}
		if req.PromptMode != agentctx.PromptModeTask {
			t.Errorf("PromptMode = %q, want task (retune wins over launch override)", req.PromptMode)
		}
	})

	t.Run("a silent retune leaves the launch override in place", func(t *testing.T) {
		l := newOverriddenLoop(t)
		if err := l.QueueRetune(retuneSpec("old task", time.Minute, 0)); err != nil {
			t.Fatalf("QueueRetune: %v", err)
		}
		req, err := l.prepareAgentTurnRequest(Request{}, "conv-1", false)
		if err != nil {
			t.Fatalf("prepareAgentTurnRequest: %v", err)
		}
		if req.PromptMode != agentctx.PromptModeFull {
			t.Errorf("PromptMode = %q, want full (launch override preserved)", req.PromptMode)
		}
	})
}

// TestQueueRetuneRejectsOperationDrift guards against the drift trap: a
// stored definition whose operation was rewritten (loop_definition_set never
// relaunches) must not conform in place — an event-driven spec carries a zero
// sleep envelope, which would turn a live timer loop into a zero-sleep
// iteration storm.
func TestQueueRetuneRejectsOperationDrift(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name:         "retune-target",
		Task:         "old task",
		SleepMin:     time.Hour,
		SleepMax:     time.Hour,
		SleepDefault: time.Hour,
		Jitter:       Float64Ptr(0),
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	drifted := Spec{
		Name:       "retune-target",
		Enabled:    true,
		Task:       "new task",
		Operation:  OperationEventDriven,
		Completion: CompletionNone,
	}
	if err := l.QueueRetune(drifted); err == nil || !strings.Contains(err.Error(), "operation") {
		t.Fatalf("QueueRetune(drifted operation) err = %v, want operation-mismatch error", err)
	}
	s := l.Status()
	if s.Config.Task != "old task" || s.Config.SleepMin != time.Hour {
		t.Errorf("config mutated by rejected drift retune: task=%q sleep_min=%v", s.Config.Task, s.Config.SleepMin)
	}
}

func TestQueueRetuneRejectsStoppedLoop(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name:         "retune-target",
		Task:         "old task",
		SleepMin:     time.Hour,
		SleepMax:     time.Hour,
		SleepDefault: time.Hour,
		Jitter:       Float64Ptr(0),
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	l.Stop()

	err = l.QueueRetune(retuneSpec("new task", time.Hour, 0))
	if err == nil {
		t.Fatal("QueueRetune on a stopped loop succeeded; want rejection so callers teach relaunch")
	}
	if s := l.Status(); s.PendingRetune {
		t.Error("pending_retune dangling on a stopped loop")
	}
}

func TestQueueRetuneRejectsFinishedLoop(t *testing.T) {
	t.Parallel()

	var count atomic.Int32
	l, err := New(Config{
		Name:         "retune-target",
		Task:         "old task",
		SleepMin:     time.Millisecond,
		SleepMax:     time.Millisecond,
		SleepDefault: time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
	}, Deps{Runner: &countingRunner{count: &count}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish its single iteration")
	}

	err = l.QueueRetune(retuneSpec("new task", time.Millisecond, 1))
	if err == nil || !strings.Contains(err.Error(), "finished") {
		t.Fatalf("QueueRetune on a finished loop err = %v, want finished-run rejection", err)
	}
	if s := l.Status(); s.PendingRetune {
		t.Error("pending_retune dangling on a finished loop")
	}
}

// TestQueueRetuneTaskClearsLaunchOverride pins the masking fix: a launch-time
// task override otherwise shadows config.Task in buildTaskTurn forever, so a
// retune that sets a task must clear it — while a task-less retune (template
// loop whose task arrives per-launch) leaves both untouched.
func TestQueueRetuneTaskClearsLaunchOverride(t *testing.T) {
	t.Parallel()

	launch := Launch{
		Spec: Spec{
			Name:         "retune-target",
			Enabled:      true,
			Operation:    OperationService,
			Completion:   CompletionNone,
			SleepMin:     time.Minute,
			SleepMax:     time.Minute,
			SleepDefault: time.Minute,
			Jitter:       Float64Ptr(0),
		},
		Task: "launch task",
	}
	l, err := NewFromLaunch(launch, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("NewFromLaunch: %v", err)
	}

	// A task-less retune (stored template spec) must not clobber the
	// launch-provided task or its override.
	template := launch.Spec
	template.SleepMin = 2 * time.Minute
	template.SleepMax = 2 * time.Minute
	template.SleepDefault = 2 * time.Minute
	if err := l.QueueRetune(template); err != nil {
		t.Fatalf("QueueRetune(task-less template): %v", err)
	}
	if got := l.Status().Config; got.Task != "launch task" || got.SleepMin != 2*time.Minute {
		t.Errorf("template retune: task=%q sleep_min=%v, want launch task preserved + envelope promoted", got.Task, got.SleepMin)
	}
	if l.taskOverride != "launch task" {
		t.Errorf("taskOverride = %q after task-less retune, want preserved", l.taskOverride)
	}

	// A retune that DOES set a task must win over the launch override.
	retuned := template
	retuned.Task = "retuned task"
	if err := l.QueueRetune(retuned); err != nil {
		t.Fatalf("QueueRetune(task): %v", err)
	}
	if got := l.Status().Config.Task; got != "retuned task" {
		t.Errorf("task = %q, want retuned", got)
	}
	if l.taskOverride != "" {
		t.Errorf("taskOverride = %q after task retune, want cleared so the retune actually wins", l.taskOverride)
	}
}

func TestQueueRetuneRejectsInvalidSpec(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name:         "retune-target",
		Task:         "old task",
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
		Jitter:       Float64Ptr(0),
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	bad := retuneSpec("new task", time.Minute, 0)
	bad.SleepMin = 10 * time.Minute
	bad.SleepMax = time.Minute
	if err := l.QueueRetune(bad); err == nil || !strings.Contains(err.Error(), "SleepMax") {
		t.Fatalf("QueueRetune(inverted envelope) err = %v, want SleepMax validation error", err)
	}
	if got := l.Status().Config.Task; got != "old task" {
		t.Errorf("config mutated by rejected retune: task = %q", got)
	}
}

// TestQueueRetunePromotesBindings pins the direction that matters for a
// boundary. An operator moving a loop from a write account to a
// read-only one is told "retune: applied"; if the old binding stayed in
// force until relaunch, the write credential would remain live behind
// that answer, and the stored definition would show the safer account
// while the running loop kept the riskier one.
func TestQueueRetunePromotesBindings(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name:         "retune-target",
		Task:         "old task",
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
		Jitter:       Float64Ptr(0),
		Bindings:     map[string]string{BindingForgeAccount: "github-primary"},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req, err := l.prepareAgentTurnRequest(Request{}, "conv-0", false)
	if err != nil {
		t.Fatalf("prepareAgentTurnRequest: %v", err)
	}
	if got := req.Bindings[BindingForgeAccount]; got != "github-primary" {
		t.Fatalf("pre-retune binding = %q, want %q", got, "github-primary")
	}

	retuned := retuneSpec("old task", time.Minute, 0)
	retuned.Bindings = map[string]string{BindingForgeAccount: "github-readonly"}
	if err := l.QueueRetune(retuned); err != nil {
		t.Fatalf("QueueRetune: %v", err)
	}

	req, err = l.prepareAgentTurnRequest(Request{}, "conv-1", false)
	if err != nil {
		t.Fatalf("prepareAgentTurnRequest: %v", err)
	}
	if got := req.Bindings[BindingForgeAccount]; got != "github-readonly" {
		t.Errorf("binding after retune = %q, want the tightened %q to take effect without relaunch", got, "github-readonly")
	}
}

// TestQueueRetuneClearsBindings covers the other end: a retune that
// declares no bindings must actually remove them rather than leave the
// previous boundary silently in force.
func TestQueueRetuneClearsBindings(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name:         "retune-target",
		Task:         "old task",
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
		Jitter:       Float64Ptr(0),
		Bindings:     map[string]string{BindingForgeAccount: "github-readonly"},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := l.QueueRetune(retuneSpec("old task", time.Minute, 0)); err != nil {
		t.Fatalf("QueueRetune: %v", err)
	}

	req, err := l.prepareAgentTurnRequest(Request{}, "conv-1", false)
	if err != nil {
		t.Fatalf("prepareAgentTurnRequest: %v", err)
	}
	if got := req.Bindings[BindingForgeAccount]; got != "" {
		t.Errorf("binding after clearing retune = %q, want it removed", got)
	}
}
