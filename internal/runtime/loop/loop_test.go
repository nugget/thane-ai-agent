package loop

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
)

// fixedRand returns a RandSource that always returns the same value.
type fixedRand struct{ val float64 }

func (f fixedRand) Float64() float64 { return f.val }

func TestClamp(t *testing.T) {
	t.Parallel()

	l := &Loop{config: Config{SleepMin: 10 * time.Second, SleepMax: 5 * time.Minute}}

	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"below min", 5 * time.Second, 10 * time.Second},
		{"at min", 10 * time.Second, 10 * time.Second},
		{"in range", 1 * time.Minute, 1 * time.Minute},
		{"at max", 5 * time.Minute, 5 * time.Minute},
		{"above max", 10 * time.Minute, 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := l.clamp(tt.in)
			if got != tt.want {
				t.Errorf("clamp(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestApplyJitter(t *testing.T) {
	t.Parallel()

	t.Run("nil jitter returns exact duration", func(t *testing.T) {
		t.Parallel()
		l := &Loop{
			config: Config{SleepMin: 10 * time.Second, SleepMax: 5 * time.Minute},
			deps:   Deps{Rand: fixedRand{0.5}},
		}
		got := l.applyJitter(1 * time.Minute)
		if got != 1*time.Minute {
			t.Errorf("applyJitter with nil jitter = %v, want 1m", got)
		}
	})

	t.Run("zero jitter returns exact duration", func(t *testing.T) {
		t.Parallel()
		l := &Loop{
			config: Config{SleepMin: 10 * time.Second, SleepMax: 5 * time.Minute, Jitter: Float64Ptr(0)},
			deps:   Deps{Rand: fixedRand{0.5}},
		}
		got := l.applyJitter(1 * time.Minute)
		if got != 1*time.Minute {
			t.Errorf("applyJitter with zero jitter = %v, want 1m", got)
		}
	})

	t.Run("jitter at midpoint returns exact duration", func(t *testing.T) {
		t.Parallel()
		// Rand=0.5 → factor = 1 + 0.2*(2*0.5-1) = 1 + 0.2*0 = 1.0
		l := &Loop{
			config: Config{SleepMin: 10 * time.Second, SleepMax: 5 * time.Minute, Jitter: Float64Ptr(0.2)},
			deps:   Deps{Rand: fixedRand{0.5}},
		}
		got := l.applyJitter(1 * time.Minute)
		if got != 1*time.Minute {
			t.Errorf("applyJitter at midpoint = %v, want 1m", got)
		}
	})

	t.Run("jitter at max returns increased duration", func(t *testing.T) {
		t.Parallel()
		// Rand=1.0 → factor = 1 + 0.2*(2*1.0-1) = 1 + 0.2*1 = 1.2
		l := &Loop{
			config: Config{SleepMin: 10 * time.Second, SleepMax: 5 * time.Minute, Jitter: Float64Ptr(0.2)},
			deps:   Deps{Rand: fixedRand{1.0}},
		}
		got := l.applyJitter(1 * time.Minute)
		want := time.Duration(float64(1*time.Minute) * 1.2)
		if got != want {
			t.Errorf("applyJitter at max = %v, want %v", got, want)
		}
	})

	t.Run("jitter at min returns decreased duration", func(t *testing.T) {
		t.Parallel()
		// Rand=0.0 → factor = 1 + 0.2*(2*0.0-1) = 1 + 0.2*(-1) = 0.8
		l := &Loop{
			config: Config{SleepMin: 10 * time.Second, SleepMax: 5 * time.Minute, Jitter: Float64Ptr(0.2)},
			deps:   Deps{Rand: fixedRand{0.0}},
		}
		got := l.applyJitter(1 * time.Minute)
		want := time.Duration(float64(1*time.Minute) * 0.8)
		if got != want {
			t.Errorf("applyJitter at min = %v, want %v", got, want)
		}
	})

	t.Run("jitter result clamped to bounds", func(t *testing.T) {
		t.Parallel()
		// With large jitter on a duration near min, result should be clamped.
		l := &Loop{
			config: Config{SleepMin: 10 * time.Second, SleepMax: 5 * time.Minute, Jitter: Float64Ptr(0.5)},
			deps:   Deps{Rand: fixedRand{0.0}}, // factor = 0.5
		}
		got := l.applyJitter(15 * time.Second) // 15s * 0.5 = 7.5s < 10s min
		if got != 10*time.Second {
			t.Errorf("applyJitter clamped = %v, want 10s", got)
		}
	})
}

func TestComputeSleep(t *testing.T) {
	t.Parallel()

	t.Run("uses default when no override", func(t *testing.T) {
		t.Parallel()
		l := &Loop{
			config: Config{
				SleepMin:     10 * time.Second,
				SleepMax:     5 * time.Minute,
				SleepDefault: 1 * time.Minute,
			},
			deps: Deps{Rand: fixedRand{0.5}},
		}
		got := l.computeSleep()
		if got != 1*time.Minute {
			t.Errorf("computeSleep = %v, want 1m", got)
		}
	})

	t.Run("uses override when set", func(t *testing.T) {
		t.Parallel()
		l := &Loop{
			config: Config{
				SleepMin:     10 * time.Second,
				SleepMax:     5 * time.Minute,
				SleepDefault: 1 * time.Minute,
			},
			deps: Deps{Rand: fixedRand{0.5}},
		}
		l.SetNextSleep(2 * time.Minute)
		got := l.computeSleep()
		if got != 2*time.Minute {
			t.Errorf("computeSleep with override = %v, want 2m", got)
		}
	})

	t.Run("exponential backoff on consecutive errors", func(t *testing.T) {
		t.Parallel()
		l := &Loop{
			config: Config{
				SleepMin:     10 * time.Second,
				SleepMax:     5 * time.Minute,
				SleepDefault: 30 * time.Second,
			},
			deps: Deps{Rand: fixedRand{0.5}},
		}

		// No errors → default sleep.
		if got := l.computeSleep(); got != 30*time.Second {
			t.Errorf("0 errors: got %v, want 30s", got)
		}

		// 1 error → 60s.
		l.consecutiveErrors = 1
		if got := l.computeSleep(); got != 60*time.Second {
			t.Errorf("1 error: got %v, want 60s", got)
		}

		// 2 errors → 120s.
		l.consecutiveErrors = 2
		if got := l.computeSleep(); got != 120*time.Second {
			t.Errorf("2 errors: got %v, want 120s", got)
		}

		// 3 errors → 240s, but capped at SleepMax (5m=300s).
		l.consecutiveErrors = 3
		if got := l.computeSleep(); got != 240*time.Second {
			t.Errorf("3 errors: got %v, want 240s", got)
		}

		// 4 errors → 480s, capped at 5m.
		l.consecutiveErrors = 4
		if got := l.computeSleep(); got != 5*time.Minute {
			t.Errorf("4 errors: got %v, want 5m (capped)", got)
		}
	})
}

func TestLoopLifecycle(t *testing.T) {
	t.Parallel()

	var iterCount atomic.Int32
	runner := &countingRunner{count: &iterCount}

	l, err := New(Config{
		Name:         "lifecycle-test",
		Task:         "test iteration",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      3,
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if l.Status().State != StatePending {
		t.Errorf("initial state = %q, want pending", l.Status().State)
	}

	ctx := context.Background()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the loop to finish (MaxIter=3).
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish within 5s")
	}

	status := l.Status()
	if status.State != StateStopped {
		t.Errorf("final state = %q, want stopped", status.State)
	}
	if status.Iterations != 3 {
		t.Errorf("iterations = %d, want 3", status.Iterations)
	}
	if status.TotalInputTokens != 30 { // 10 per iter * 3
		t.Errorf("TotalInputTokens = %d, want 30", status.TotalInputTokens)
	}
	if int(iterCount.Load()) != 3 {
		t.Errorf("runner call count = %d, want 3", iterCount.Load())
	}
}

func TestLoopStopCancels(t *testing.T) {
	t.Parallel()

	runner := &countingRunner{count: &atomic.Int32{}}

	l, err := New(Config{
		Name:         "stop-test",
		Task:         "test",
		SleepMin:     1 * time.Hour, // long sleep so it's sleeping when we stop
		SleepMax:     1 * time.Hour,
		SleepDefault: 1 * time.Hour,
		Jitter:       Float64Ptr(0),
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	_ = l.Start(ctx)

	// Give it time to run one iteration and enter sleep.
	time.Sleep(50 * time.Millisecond)

	l.Stop()

	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop within 5s")
	}

	if l.Status().State != StateStopped {
		t.Errorf("state after stop = %q, want stopped", l.Status().State)
	}
}

func TestLoopMaxDuration(t *testing.T) {
	t.Parallel()

	runner := &countingRunner{count: &atomic.Int32{}}

	l, err := New(Config{
		Name:         "duration-test",
		Task:         "test",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxDuration:  50 * time.Millisecond,
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	_ = l.Start(ctx)

	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish within 5s")
	}

	if l.Status().State != StateStopped {
		t.Errorf("state = %q, want stopped", l.Status().State)
	}
}

func TestLoopPublishesEvents(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ch := bus.Subscribe(64)
	defer bus.Unsubscribe(ch)

	runner := &countingRunner{count: &atomic.Int32{}}

	l, err := New(Config{
		Name:         "event-test",
		Task:         "test",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
	}, Deps{Runner: runner, EventBus: bus})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	_ = l.Start(ctx)

	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish within 5s")
	}

	// Drain events and check we got the expected kinds.
	var kinds []string
	for {
		select {
		case e := <-ch:
			if e.Source == events.SourceLoop {
				kinds = append(kinds, e.Kind)
			}
		default:
			goto done
		}
	}
done:

	// We expect at minimum: state_change (pending→sleeping), loop_started,
	// state_change (sleeping→processing), loop_iteration_start,
	// loop_iteration_complete, state_change (processing→sleeping),
	// loop_sleep_start... and eventually loop_stopped.
	hasStarted := false
	hasStopped := false
	hasIterStart := false
	hasIterComplete := false
	for _, k := range kinds {
		switch k {
		case events.KindLoopStarted:
			hasStarted = true
		case events.KindLoopStopped:
			hasStopped = true
		case events.KindLoopIterationStart:
			hasIterStart = true
		case events.KindLoopIterationComplete:
			hasIterComplete = true
		}
	}

	if !hasStarted {
		t.Error("missing loop_started event")
	}
	if !hasStopped {
		t.Error("missing loop_stopped event")
	}
	if !hasIterStart {
		t.Error("missing loop_iteration_start event")
	}
	if !hasIterComplete {
		t.Error("missing loop_iteration_complete event")
	}
}

func TestLoopSupervisorDice(t *testing.T) {
	t.Parallel()

	t.Run("never supervisor when disabled", func(t *testing.T) {
		t.Parallel()

		var supervisorSeen atomic.Bool
		runner := &inspectingRunner{
			onRun: func(req RunRequest) {
				if req.Hints["supervisor"] == "true" {
					supervisorSeen.Store(true)
				}
			},
		}

		l, err := New(Config{
			Name:         "no-supervisor",
			Task:         "test",
			SleepMin:     1 * time.Millisecond,
			SleepMax:     2 * time.Millisecond,
			SleepDefault: 1 * time.Millisecond,
			Jitter:       Float64Ptr(0),
			MaxIter:      5,
			Supervisor:   false,
		}, Deps{Runner: runner, Rand: fixedRand{0.0}}) // would be supervisor if enabled
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		_ = l.Start(context.Background())
		<-l.Done()

		if supervisorSeen.Load() {
			t.Error("supervisor seen when disabled")
		}
	})

	t.Run("always supervisor when prob=1", func(t *testing.T) {
		t.Parallel()

		var supervisorCount atomic.Int32
		runner := &inspectingRunner{
			onRun: func(req RunRequest) {
				if req.Hints["supervisor"] == "true" {
					supervisorCount.Add(1)
				}
			},
		}

		l, err := New(Config{
			Name:           "always-supervisor",
			Task:           "test",
			SleepMin:       1 * time.Millisecond,
			SleepMax:       2 * time.Millisecond,
			SleepDefault:   1 * time.Millisecond,
			Jitter:         Float64Ptr(0),
			MaxIter:        3,
			Supervisor:     true,
			SupervisorProb: 1.0,
		}, Deps{Runner: runner, Rand: fixedRand{0.5}})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		_ = l.Start(context.Background())
		<-l.Done()

		if supervisorCount.Load() != 3 {
			t.Errorf("supervisor count = %d, want 3", supervisorCount.Load())
		}
	})
}

func TestErrorBackoffBehavior(t *testing.T) {
	t.Parallel()

	// failingRunner errors for failCount calls, then succeeds.
	var callNum atomic.Int32
	const failCount = 3
	runner := &callbackRunner{
		fn: func(_ context.Context, _ RunRequest) (*RunResponse, error) {
			n := callNum.Add(1)
			if n <= failCount {
				return nil, fmt.Errorf("simulated failure %d", n)
			}
			return &RunResponse{
				Content:      "ok",
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
			}, nil
		},
	}

	l, err := New(Config{
		Name:         "backoff-test",
		Task:         "test",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     100 * time.Millisecond,
		SleepDefault: 5 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      5, // 3 failures + 2 successes
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())

	select {
	case <-l.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("loop did not finish within 10s")
	}

	status := l.Status()

	// We expect 3 failures + 2 successes = 5 attempts, 2 iterations.
	if status.Attempts != 5 {
		t.Errorf("attempts = %d, want 5", status.Attempts)
	}
	if status.Iterations != 2 {
		t.Errorf("iterations = %d, want 2", status.Iterations)
	}
	// After a success, consecutiveErrors should be reset.
	if l.consecutiveErrors != 0 {
		t.Errorf("consecutiveErrors = %d, want 0 (reset after success)", l.consecutiveErrors)
	}
	// LastError should be empty after a successful final iteration.
	if status.LastError != "" {
		t.Errorf("lastError = %q, want empty", status.LastError)
	}
}

func TestBackoffCapsAtSleepMax(t *testing.T) {
	t.Parallel()

	l := &Loop{
		config: Config{
			SleepMin:     1 * time.Second,
			SleepMax:     10 * time.Second,
			SleepDefault: 1 * time.Second,
		},
		deps: Deps{Rand: fixedRand{0.5}},
	}

	// With many errors, backoff should never exceed SleepMax.
	l.consecutiveErrors = 100
	got := l.computeSleep()
	if got > l.config.SleepMax {
		t.Errorf("backoff with 100 errors = %v, exceeds SleepMax %v", got, l.config.SleepMax)
	}
	if got != l.config.SleepMax {
		t.Errorf("backoff with 100 errors = %v, want SleepMax %v", got, l.config.SleepMax)
	}
}

func TestDoubleStartIsNoop(t *testing.T) {
	t.Parallel()

	runner := &countingRunner{count: &atomic.Int32{}}
	l, err := New(Config{
		Name:         "double-start",
		Task:         "test",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	_ = l.Start(ctx)
	err = l.Start(ctx) // should be no-op
	if err != nil {
		t.Errorf("second Start returned error: %v", err)
	}

	<-l.Done()
}

func TestStopBeforeStartPreventsStart(t *testing.T) {
	t.Parallel()

	l, err := New(Config{Name: "never-started", Task: "test"}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	l.Stop() // should not panic

	if l.Done() != nil {
		t.Error("Done() should be nil before Start")
	}

	// Attempting to start after Stop should fail.
	err = l.Start(context.Background())
	if err != ErrLoopStopped {
		t.Errorf("Start after Stop: got %v, want ErrLoopStopped", err)
	}
}

func TestNewRequiresRunner(t *testing.T) {
	t.Parallel()

	_, err := New(Config{Name: "no-runner", Task: "test"}, Deps{})
	if err != ErrNilRunner {
		t.Errorf("New without runner: got %v, want ErrNilRunner", err)
	}
}

func TestNewValidation(t *testing.T) {
	t.Parallel()

	runner := &noopRunner{}

	t.Run("empty name", func(t *testing.T) {
		t.Parallel()
		_, err := New(Config{}, Deps{Runner: runner})
		if err == nil {
			t.Error("expected error for empty Name")
		}
	})

	t.Run("empty task", func(t *testing.T) {
		t.Parallel()
		_, err := New(Config{Name: "no-task"}, Deps{Runner: runner})
		if err == nil {
			t.Error("expected error for empty Task")
		}
	})

	t.Run("SleepMax less than SleepMin", func(t *testing.T) {
		t.Parallel()
		_, err := New(Config{
			Name:     "bad-sleep",
			Task:     "test",
			SleepMin: 5 * time.Minute,
			SleepMax: 1 * time.Second,
		}, Deps{Runner: runner})
		if err == nil {
			t.Error("expected error for SleepMax < SleepMin")
		}
	})

	t.Run("jitter out of range", func(t *testing.T) {
		t.Parallel()
		_, err := New(Config{
			Name:   "bad-jitter",
			Task:   "test",
			Jitter: Float64Ptr(1.5),
		}, Deps{Runner: runner})
		if err == nil {
			t.Error("expected error for Jitter > 1")
		}
	})

	t.Run("supervisor prob out of range", func(t *testing.T) {
		t.Parallel()
		_, err := New(Config{
			Name:           "bad-prob",
			Task:           "test",
			SupervisorProb: 2.0,
		}, Deps{Runner: runner})
		if err == nil {
			t.Error("expected error for SupervisorProb > 1")
		}
	})
}

func TestTaskBuilder(t *testing.T) {
	t.Parallel()

	var callNum atomic.Int32
	runner := &inspectingRunner{
		onRun: func(req RunRequest) {
			// Verify the dynamic prompt is used.
			n := callNum.Add(1)
			want := fmt.Sprintf("dynamic prompt %d", n)
			if len(req.Messages) == 0 || req.Messages[0].Content != want {
				t.Errorf("iteration %d: got prompt %q, want %q",
					n, req.Messages[0].Content, want)
			}
		},
	}

	var builderCalls atomic.Int32
	l, err := New(Config{
		Name:         "task-builder",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      3,
		TaskBuilder: func(_ context.Context, _ bool) (string, error) {
			n := builderCalls.Add(1)
			return fmt.Sprintf("dynamic prompt %d", n), nil
		},
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())
	<-l.Done()

	if builderCalls.Load() != 3 {
		t.Errorf("TaskBuilder called %d times, want 3", builderCalls.Load())
	}
}

func TestTaskBuilderError(t *testing.T) {
	t.Parallel()

	runner := &countingRunner{count: &atomic.Int32{}}

	l, err := New(Config{
		Name:         "task-builder-err",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      2,
		TaskBuilder: func(_ context.Context, _ bool) (string, error) {
			return "", fmt.Errorf("build failed")
		},
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())
	<-l.Done()

	status := l.Status()
	// Both iterations should fail (TaskBuilder errors count as failures).
	if status.Iterations != 0 {
		t.Errorf("iterations = %d, want 0", status.Iterations)
	}
	if status.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", status.Attempts)
	}
	// Runner should never have been called.
	if runner.count.Load() != 0 {
		t.Errorf("runner called %d times, want 0", runner.count.Load())
	}
}

func TestTaskBuilderValidation(t *testing.T) {
	t.Parallel()

	runner := &noopRunner{}

	t.Run("TaskBuilder accepted without Task", func(t *testing.T) {
		t.Parallel()
		_, err := New(Config{
			Name: "builder-only",
			TaskBuilder: func(_ context.Context, _ bool) (string, error) {
				return "dynamic", nil
			},
		}, Deps{Runner: runner})
		if err != nil {
			t.Errorf("expected no error with TaskBuilder, got: %v", err)
		}
	})

	t.Run("TurnBuilder accepted without Task", func(t *testing.T) {
		t.Parallel()
		_, err := New(Config{
			Name: "turn-builder-only",
			TurnBuilder: func(context.Context, TurnInput) (*AgentTurn, error) {
				return &AgentTurn{
					Request: Request{
						Messages: []Message{{Role: "user", Content: "dynamic"}},
					},
				}, nil
			},
		}, Deps{Runner: runner})
		if err != nil {
			t.Errorf("expected no error with TurnBuilder, got: %v", err)
		}
	})

	t.Run("neither Task nor builder nor Handler fails", func(t *testing.T) {
		t.Parallel()
		_, err := New(Config{Name: "neither"}, Deps{Runner: runner})
		if err == nil {
			t.Error("expected error when neither Task, builder, nor Handler set")
		}
	})
}

func TestTurnBuilderRunsThroughLoopRunner(t *testing.T) {
	t.Parallel()

	var gotReq RunRequest
	runner := &inspectingRunner{onRun: func(req RunRequest) {
		gotReq = req
	}}

	l, err := New(Config{
		Name:         "turn-builder",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
		Tags:         []string{"email"},
		TurnBuilder: func(context.Context, TurnInput) (*AgentTurn, error) {
			return &AgentTurn{
				Request: Request{
					ConversationID: "email-poll-123",
					Messages:       []Message{{Role: "user", Content: "triage this mail"}},
					Hints:          map[string]string{"source": "email_poll"},
				},
				Summary: map[string]any{"wake_msg_len": 42},
			}, nil
		},
	}, Deps{Runner: runner, EventBus: events.New()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())
	<-l.Done()

	if gotReq.ConversationID != "email-poll-123" {
		t.Fatalf("ConversationID = %q, want email-poll-123", gotReq.ConversationID)
	}
	if len(gotReq.Messages) != 1 || gotReq.Messages[0].Content != "triage this mail" {
		t.Fatalf("Messages = %#v, want triage prompt", gotReq.Messages)
	}
	if gotReq.Hints["source"] != "email_poll" {
		t.Fatalf("source hint = %q, want email_poll", gotReq.Hints["source"])
	}
	if gotReq.Hints["loop_name"] != "turn-builder" {
		t.Fatalf("loop_name hint = %q, want turn-builder", gotReq.Hints["loop_name"])
	}
	if !slices.Equal(gotReq.InitialTags, []string{"email"}) {
		t.Fatalf("InitialTags = %v, want [email]", gotReq.InitialTags)
	}
	if gotReq.OnProgress == nil {
		t.Fatal("OnProgress is nil, want loop progress wiring")
	}

	status := l.Status()
	if status.HandlerOnly {
		t.Fatal("HandlerOnly = true, want false for turn builder")
	}
	if status.Iterations != 1 || status.Attempts != 1 {
		t.Fatalf("Iterations/Attempts = %d/%d, want 1/1", status.Iterations, status.Attempts)
	}
	if len(status.RecentIterations) != 1 {
		t.Fatalf("RecentIterations = %d, want 1", len(status.RecentIterations))
	}
	snap := status.RecentIterations[0]
	if snap.ConvID != "email-poll-123" {
		t.Fatalf("snapshot ConvID = %q, want email-poll-123", snap.ConvID)
	}
	if snap.Summary["wake_msg_len"] != 42 {
		t.Fatalf("snapshot wake_msg_len = %v, want 42", snap.Summary["wake_msg_len"])
	}
}

func TestTurnBuilderAllowedToolsOverrideIntersects(t *testing.T) {
	t.Parallel()

	var gotReq RunRequest
	runner := &inspectingRunner{onRun: func(req RunRequest) {
		gotReq = req
	}}

	l, err := New(Config{
		Name:         "agent-turn-allowed-tools",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
		TurnBuilder: func(context.Context, TurnInput) (*AgentTurn, error) {
			return &AgentTurn{
				Request: Request{
					ConversationID: "allowed-tools-123",
					Messages:       []Message{{Role: "user", Content: "use scoped tools"}},
					AllowedTools:   []string{"email_search", "email_archive"},
				},
			}, nil
		},
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	l.requestOverride = Request{AllowedTools: []string{"email_search", "shell_exec"}}

	_ = l.Start(context.Background())
	<-l.Done()

	if !slices.Equal(gotReq.AllowedTools, []string{"email_search"}) {
		t.Fatalf("AllowedTools = %#v, want intersection [email_search]", gotReq.AllowedTools)
	}
}

func TestTurnBuilderAllowedToolsOverrideFailsClosed(t *testing.T) {
	t.Parallel()

	var runnerCalls atomic.Int32
	l, err := New(Config{
		Name:         "agent-turn-allowed-tools-empty",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
		TurnBuilder: func(context.Context, TurnInput) (*AgentTurn, error) {
			return &AgentTurn{
				Request: Request{
					ConversationID: "allowed-tools-empty-123",
					Messages:       []Message{{Role: "user", Content: "use scoped tools"}},
					AllowedTools:   []string{"email_search"},
				},
			}, nil
		},
	}, Deps{Runner: &countingRunner{count: &runnerCalls}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	l.requestOverride = Request{AllowedTools: []string{"shell_exec"}}

	_ = l.Start(context.Background())
	<-l.Done()

	status := l.Status()
	if runnerCalls.Load() != 0 {
		t.Fatalf("runner calls = %d, want 0", runnerCalls.Load())
	}
	if status.Iterations != 0 || status.Attempts != 1 {
		t.Fatalf("Iterations/Attempts = %d/%d, want 0/1", status.Iterations, status.Attempts)
	}
	if !strings.Contains(status.LastError, "allowed_tools override has no overlap") {
		t.Fatalf("LastError = %q, want allowed_tools overlap error", status.LastError)
	}
}

func TestTurnBuilderNilTurnIsNoOp(t *testing.T) {
	t.Parallel()

	var runnerCalls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l, err := New(Config{
		Name:         "turn-noop",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      100,
		TurnBuilder: func(context.Context, TurnInput) (*AgentTurn, error) {
			cancel()
			return nil, nil
		},
	}, Deps{Runner: &countingRunner{count: &runnerCalls}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(ctx)
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish")
	}

	status := l.Status()
	if runnerCalls.Load() != 0 {
		t.Fatalf("runner calls = %d, want 0", runnerCalls.Load())
	}
	if status.Iterations != 0 || status.Attempts != 0 {
		t.Fatalf("Iterations/Attempts = %d/%d, want 0/0", status.Iterations, status.Attempts)
	}
	if status.ConsecutiveErrors != 0 || status.LastError != "" {
		t.Fatalf("error state = %d/%q, want empty", status.ConsecutiveErrors, status.LastError)
	}
}

func TestPostIterate(t *testing.T) {
	t.Parallel()

	var results []IterationResult
	var mu sync.Mutex

	l, err := New(Config{
		Name:         "post-iterate",
		Task:         "test",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      2,
		PostIterate: func(_ context.Context, result IterationResult) error {
			mu.Lock()
			results = append(results, result)
			mu.Unlock()
			return nil
		},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())
	<-l.Done()

	mu.Lock()
	defer mu.Unlock()

	if len(results) != 2 {
		t.Fatalf("PostIterate called %d times, want 2", len(results))
	}
	for i, r := range results {
		if r.ConvID == "" {
			t.Errorf("result[%d].ConvID is empty", i)
		}
		if r.Model != "test-model" {
			t.Errorf("result[%d].Model = %q, want test-model", i, r.Model)
		}
		if r.Sleep <= 0 {
			t.Errorf("result[%d].Sleep = %v, want positive", i, r.Sleep)
		}
	}
}

func TestPostIterateError(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name:         "post-iterate-err",
		Task:         "test",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      2,
		PostIterate: func(_ context.Context, _ IterationResult) error {
			return fmt.Errorf("post-iterate failed")
		},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())
	<-l.Done()

	status := l.Status()
	// PostIterate errors should not count as failures.
	if status.Iterations != 2 {
		t.Errorf("iterations = %d, want 2 (PostIterate errors shouldn't affect count)", status.Iterations)
	}
	if status.LastError != "" {
		t.Errorf("lastError = %q, want empty", status.LastError)
	}
}

func TestHintsMerge(t *testing.T) {
	t.Parallel()

	var capturedHints map[string]string
	var mu sync.Mutex

	runner := &inspectingRunner{
		onRun: func(req RunRequest) {
			mu.Lock()
			capturedHints = req.Hints
			mu.Unlock()
		},
	}

	l, err := New(Config{
		Name:         "hints-merge",
		Task:         "test",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
		Hints: map[string]string{
			"source":  "metacognitive", // overrides loop default
			"mission": "reflect",       // new hint
		},
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())
	<-l.Done()

	mu.Lock()
	defer mu.Unlock()

	if capturedHints == nil {
		t.Fatal("hints not captured")
	}
	// Config hint should override loop default.
	if capturedHints["source"] != "metacognitive" {
		t.Errorf("source hint = %q, want metacognitive", capturedHints["source"])
	}
	// New hint should be present.
	if capturedHints["mission"] != "reflect" {
		t.Errorf("mission hint = %q, want reflect", capturedHints["mission"])
	}
	// Loop-generated hints should still be present.
	if capturedHints["loop_name"] != "hints-merge" {
		t.Errorf("loop_name hint = %q, want hints-merge", capturedHints["loop_name"])
	}
}

func TestNewFromSpecAppliesProfileToRequest(t *testing.T) {
	t.Parallel()

	var captured Request
	var mu sync.Mutex

	runner := &inspectingRunner{
		onRun: func(req RunRequest) {
			mu.Lock()
			captured = Request{
				Model:         req.Model,
				Messages:      append([]Message(nil), req.Messages...),
				ExcludeTools:  append([]string(nil), req.ExcludeTools...),
				InitialTags:   append([]string(nil), req.InitialTags...),
				SkipTagFilter: req.SkipTagFilter,
				Hints:         cloneStringMap(req.Hints),
			}
			mu.Unlock()
		},
	}

	l, err := NewFromSpec(Spec{
		Name:         "spec-profile",
		Task:         "evaluate the alert",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
		Tags:         []string{"homeassistant"},
		Profile: router.LoopProfile{
			Model:        "spark/gpt-oss:20b",
			Mission:      "automation",
			ExcludeTools: []string{"shell_exec"},
			Instructions: "stay concise",
			PreferSpeed:  "true",
			LocalOnly:    "false",
			QualityFloor: "7",
			ExtraHints:   map[string]string{"source": "profile"},
		},
		ExcludeTools: []string{"dangerous_tool"},
		Hints: map[string]string{
			"source": "spec",
		},
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}

	_ = l.Start(context.Background())
	<-l.Done()

	mu.Lock()
	defer mu.Unlock()

	if captured.Model != "spark/gpt-oss:20b" {
		t.Fatalf("Model = %q, want spark/gpt-oss:20b", captured.Model)
	}
	if len(captured.Messages) == 0 {
		t.Fatal("Messages empty")
	}
	if got := captured.Messages[0].Content; got != "Instructions: stay concise\n\nevaluate the alert" {
		t.Fatalf("Message content = %q", got)
	}
	if captured.Hints["mission"] != "automation" {
		t.Fatalf("mission hint = %q, want automation", captured.Hints["mission"])
	}
	if captured.Hints["quality_floor"] != "7" {
		t.Fatalf("quality_floor hint = %q, want 7", captured.Hints["quality_floor"])
	}
	if captured.Hints["prefer_speed"] != "true" {
		t.Fatalf("prefer_speed hint = %q, want true", captured.Hints["prefer_speed"])
	}
	if captured.Hints["source"] != "spec" {
		t.Fatalf("source hint = %q, want spec", captured.Hints["source"])
	}
	if !slices.Contains(captured.ExcludeTools, "shell_exec") || !slices.Contains(captured.ExcludeTools, "dangerous_tool") {
		t.Fatalf("ExcludeTools = %#v", captured.ExcludeTools)
	}
	if !slices.Contains(captured.InitialTags, "homeassistant") {
		t.Fatalf("InitialTags = %#v", captured.InitialTags)
	}
}

func TestConfigTagsSeedRequestInitialTags(t *testing.T) {
	t.Parallel()

	var captured Request
	var mu sync.Mutex
	runner := &inspectingRunner{
		onRun: func(req RunRequest) {
			mu.Lock()
			defer mu.Unlock()
			captured = Request{
				InitialTags:   append([]string(nil), req.InitialTags...),
				SkipTagFilter: req.SkipTagFilter,
			}
		},
	}

	l, err := New(Config{
		Name:         "config-tags",
		Task:         "test",
		Tags:         []string{"ha", "documents"},
		SleepMin:     time.Millisecond,
		SleepMax:     time.Millisecond,
		SleepDefault: time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish")
	}

	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(captured.InitialTags, "ha") || !slices.Contains(captured.InitialTags, "documents") {
		t.Fatalf("InitialTags = %v, want ha and documents", captured.InitialTags)
	}
	if captured.SkipTagFilter {
		t.Fatal("SkipTagFilter = true, want false when config tags define loop scope")
	}
}

func TestStatusToolingIncludesConfiguredAndLaunchTags(t *testing.T) {
	t.Parallel()

	l, err := NewFromLaunch(Launch{
		Spec: Spec{
			Name:       "status-tags",
			Task:       "test",
			Operation:  OperationRequestReply,
			Completion: CompletionReturn,
			Tags:       []string{"ha"},
		},
		InitialTags: []string{"documents"},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("NewFromLaunch: %v", err)
	}

	status := l.Status()
	if !slices.Contains(status.ActiveTags, "ha") || !slices.Contains(status.ActiveTags, "documents") {
		t.Fatalf("ActiveTags = %v, want ha and documents before first iteration", status.ActiveTags)
	}
	if !slices.Contains(status.Tooling.ConfiguredTags, "ha") || !slices.Contains(status.Tooling.ConfiguredTags, "documents") {
		t.Fatalf("Tooling.ConfiguredTags = %v, want ha and documents", status.Tooling.ConfiguredTags)
	}
	if !slices.Contains(status.Tooling.LoadedTags, "ha") || !slices.Contains(status.Tooling.LoadedTags, "documents") {
		t.Fatalf("Tooling.LoadedTags = %v, want ha and documents", status.Tooling.LoadedTags)
	}
}

func TestCurrentConvID(t *testing.T) {
	t.Parallel()

	var capturedConvID string
	var mu sync.Mutex

	runner := &callbackRunner{
		fn: func(_ context.Context, _ RunRequest) (*RunResponse, error) {
			// Simulate a tool handler reading CurrentConvID during iteration.
			// We need to access the loop, so we'll capture it via closure below.
			return &RunResponse{
				Content:      "ok",
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
			}, nil
		},
	}

	l, err := New(Config{
		Name:         "conv-id-test",
		Task:         "test",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Replace the runner with one that captures convID from the loop.
	l.deps.Runner = &callbackRunner{
		fn: func(_ context.Context, _ RunRequest) (*RunResponse, error) {
			mu.Lock()
			capturedConvID = l.CurrentConvID()
			mu.Unlock()
			return &RunResponse{
				Content:      "ok",
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
			}, nil
		},
	}

	_ = l.Start(context.Background())
	<-l.Done()

	mu.Lock()
	defer mu.Unlock()

	if capturedConvID == "" {
		t.Error("CurrentConvID was empty during iteration")
	}

	// After iteration completes, convID should be cleared.
	if l.CurrentConvID() != "" {
		t.Errorf("CurrentConvID after done = %q, want empty", l.CurrentConvID())
	}
}

func TestSetupCallback(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	var setupCalled bool
	var setupLoop *Loop

	cfg := Config{
		Name:         "setup-test",
		Task:         "test",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
		Setup: func(l *Loop) {
			setupCalled = true
			setupLoop = l
		},
	}

	id, err := r.SpawnLoop(context.Background(), cfg, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("SpawnLoop: %v", err)
	}

	if !setupCalled {
		t.Error("Setup callback was not called")
	}
	if setupLoop == nil {
		t.Fatal("Setup callback received nil loop")
	}
	if setupLoop.ID() != id {
		t.Errorf("Setup loop ID = %q, want %q", setupLoop.ID(), id)
	}

	// Wait for loop to finish.
	l := r.Get(id)
	if l != nil {
		select {
		case <-l.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("loop did not finish")
		}
	}
}

// countingRunner counts Run calls and returns minimal responses.
type countingRunner struct {
	count *atomic.Int32
}

func (r *countingRunner) Run(_ context.Context, _ RunRequest, _ StreamCallback) (*RunResponse, error) {
	r.count.Add(1)
	return &RunResponse{
		Content:      "ok",
		Model:        "test-model",
		InputTokens:  10,
		OutputTokens: 5,
	}, nil
}

// inspectingRunner calls a callback with each request for inspection.
type inspectingRunner struct {
	onRun func(RunRequest)
}

func (r *inspectingRunner) Run(_ context.Context, req RunRequest, _ StreamCallback) (*RunResponse, error) {
	if r.onRun != nil {
		r.onRun(req)
	}
	return &RunResponse{
		Content:      "ok",
		Model:        "test-model",
		InputTokens:  10,
		OutputTokens: 5,
	}, nil
}

func TestRecentConvIDs(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name:         "convid-ring",
		Task:         "test",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      3,
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())
	<-l.Done()

	s := l.Status()
	if len(s.RecentConvIDs) != 3 {
		t.Fatalf("RecentConvIDs has %d entries, want 3", len(s.RecentConvIDs))
	}

	// Newest first: index 0 should be the last iteration's convID.
	for i := 1; i < len(s.RecentConvIDs); i++ {
		if s.RecentConvIDs[i] == s.RecentConvIDs[i-1] {
			t.Errorf("RecentConvIDs[%d] == RecentConvIDs[%d] = %q (should be unique)", i, i-1, s.RecentConvIDs[i])
		}
	}
}

func TestRecentConvIDsCap(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name:         "convid-cap",
		Task:         "test",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      recentConvIDsCap + 5,
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())
	<-l.Done()

	s := l.Status()
	if len(s.RecentConvIDs) != recentConvIDsCap {
		t.Fatalf("RecentConvIDs has %d entries, want cap %d", len(s.RecentConvIDs), recentConvIDsCap)
	}
}

// callbackRunner delegates Run to a caller-provided function.
type callbackRunner struct {
	fn func(context.Context, RunRequest) (*RunResponse, error)
}

func (r *callbackRunner) Run(ctx context.Context, req RunRequest, _ StreamCallback) (*RunResponse, error) {
	return r.fn(ctx, req)
}

// ---------------------------------------------------------------------------
// Handler + WaitFunc tests
// ---------------------------------------------------------------------------

func TestHandlerTimerLoop(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	var receivedEvents []any
	var mu sync.Mutex

	l, err := New(Config{
		Name:         "handler-timer",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      3,
		Handler: func(_ context.Context, event any) error {
			mu.Lock()
			receivedEvents = append(receivedEvents, event)
			mu.Unlock()
			calls.Add(1)
			return nil
		},
	}, Deps{}) // No Runner needed
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())
	<-l.Done()

	status := l.Status()
	if status.Iterations != 3 {
		t.Errorf("iterations = %d, want 3", status.Iterations)
	}
	if status.TotalInputTokens != 0 {
		t.Errorf("TotalInputTokens = %d, want 0 (handler-only)", status.TotalInputTokens)
	}
	if status.TotalOutputTokens != 0 {
		t.Errorf("TotalOutputTokens = %d, want 0 (handler-only)", status.TotalOutputTokens)
	}
	if int(calls.Load()) != 3 {
		t.Errorf("handler call count = %d, want 3", calls.Load())
	}
	if !status.HandlerOnly {
		t.Error("HandlerOnly = false, want true")
	}
	if status.EventDriven {
		t.Error("EventDriven = true, want false (timer-driven)")
	}

	// Timer-driven handler loops should receive nil events.
	mu.Lock()
	defer mu.Unlock()
	for i, ev := range receivedEvents {
		if ev != nil {
			t.Errorf("receivedEvents[%d] = %v, want nil", i, ev)
		}
	}
}

func TestHandlerError(t *testing.T) {
	t.Parallel()

	var callNum atomic.Int32
	l, err := New(Config{
		Name:         "handler-err",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     100 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      3,
		Handler: func(_ context.Context, _ any) error {
			n := callNum.Add(1)
			if n <= 2 {
				return fmt.Errorf("fail %d", n)
			}
			return nil
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())
	<-l.Done()

	status := l.Status()
	if status.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", status.Attempts)
	}
	if status.Iterations != 1 {
		t.Errorf("iterations = %d, want 1", status.Iterations)
	}
	if status.ConsecutiveErrors != 0 {
		t.Errorf("consecutiveErrors = %d, want 0 (reset after success)", status.ConsecutiveErrors)
	}
	if status.LastError != "" {
		t.Errorf("lastError = %q, want empty", status.LastError)
	}
}

func TestHandlerNoRunnerRequired(t *testing.T) {
	t.Parallel()

	_, err := New(Config{
		Name: "handler-no-runner",
		Handler: func(_ context.Context, _ any) error {
			return nil
		},
	}, Deps{})
	if err != nil {
		t.Errorf("expected no error with Handler and no Runner, got: %v", err)
	}
}

func TestHandlerNoRunnerNoHandlerFails(t *testing.T) {
	t.Parallel()

	_, err := New(Config{Name: "no-handler-no-runner", Task: "test"}, Deps{})
	if err != ErrNilRunner {
		t.Errorf("expected ErrNilRunner, got: %v", err)
	}
}

func TestWaitFuncWithHandler(t *testing.T) {
	t.Parallel()

	var waitNum atomic.Int32
	var receivedEvents []any
	var mu sync.Mutex

	l, err := New(Config{
		Name:    "wait-handler",
		MaxIter: 3,
		WaitFunc: func(ctx context.Context) (any, error) {
			n := waitNum.Add(1)
			return fmt.Sprintf("event-%d", n), nil
		},
		Handler: func(_ context.Context, event any) error {
			mu.Lock()
			receivedEvents = append(receivedEvents, event)
			mu.Unlock()
			return nil
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())

	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish within 5s")
	}

	status := l.Status()
	if status.Iterations != 3 {
		t.Errorf("iterations = %d, want 3", status.Iterations)
	}
	if !status.HandlerOnly {
		t.Error("HandlerOnly = false, want true")
	}
	if !status.EventDriven {
		t.Error("EventDriven = false, want true")
	}

	// WaitFunc loops wait-at-top: all 3 iterations should get events.
	mu.Lock()
	defer mu.Unlock()
	if len(receivedEvents) != 3 {
		t.Fatalf("received %d events, want 3", len(receivedEvents))
	}
	for i, ev := range receivedEvents {
		want := fmt.Sprintf("event-%d", i+1)
		if ev != want {
			t.Errorf("receivedEvents[%d] = %v, want %q", i, ev, want)
		}
	}
}

func TestWaitFuncWithTask(t *testing.T) {
	t.Parallel()

	var waitNum atomic.Int32
	runner := &countingRunner{count: &atomic.Int32{}}

	l, err := New(Config{
		Name:    "wait-task",
		Task:    "test",
		MaxIter: 3,
		WaitFunc: func(ctx context.Context) (any, error) {
			waitNum.Add(1)
			return "trigger", nil
		},
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())

	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish within 5s")
	}

	status := l.Status()
	if status.Iterations != 3 {
		t.Errorf("iterations = %d, want 3", status.Iterations)
	}
	// Runner should have been called for all 3 iterations.
	if runner.count.Load() != 3 {
		t.Errorf("runner calls = %d, want 3", runner.count.Load())
	}
	// WaitFunc should have been called 3 times (once per iteration,
	// at the top of the loop).
	if waitNum.Load() != 3 {
		t.Errorf("WaitFunc calls = %d, want 3", waitNum.Load())
	}
	if status.HandlerOnly {
		t.Error("HandlerOnly = true, want false")
	}
	if !status.EventDriven {
		t.Error("EventDriven = false, want true")
	}
}

func TestWaitFuncError(t *testing.T) {
	t.Parallel()

	var waitNum atomic.Int32
	l, err := New(Config{
		Name:         "wait-err",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      2,
		WaitFunc: func(ctx context.Context) (any, error) {
			n := waitNum.Add(1)
			if n == 1 {
				return nil, fmt.Errorf("connection lost")
			}
			return "ok", nil
		},
		Handler: func(_ context.Context, _ any) error {
			return nil
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())

	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish within 5s")
	}

	status := l.Status()
	// First WaitFunc fails (no iteration counted), then succeeds and
	// handler runs twice (MaxIter=2).
	if status.Iterations != 2 {
		t.Errorf("iterations = %d, want 2", status.Iterations)
	}
	// After success, errors should be cleared.
	if status.ConsecutiveErrors != 0 {
		t.Errorf("consecutiveErrors = %d, want 0", status.ConsecutiveErrors)
	}
}

func TestWaitFuncContextCanceledStopsLoop(t *testing.T) {
	t.Parallel()

	var handled atomic.Int32
	l, err := New(Config{
		Name:         "wait-cancel",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		WaitFunc: func(context.Context) (any, error) {
			return nil, context.Canceled
		},
		Handler: func(_ context.Context, _ any) error {
			handled.Add(1)
			return nil
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())

	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish within 5s")
	}

	status := l.Status()
	if status.Iterations != 0 {
		t.Errorf("iterations = %d, want 0", status.Iterations)
	}
	if handled.Load() != 0 {
		t.Errorf("handled = %d, want 0", handled.Load())
	}
}

func TestWaitFuncPublishesWaitEvent(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ch := bus.Subscribe(64)
	defer bus.Unsubscribe(ch)

	var waitNum atomic.Int32
	l, err := New(Config{
		Name:    "wait-event",
		MaxIter: 1,
		WaitFunc: func(ctx context.Context) (any, error) {
			waitNum.Add(1)
			return "event", nil // non-nil payload triggers processing
		},
		Handler: func(_ context.Context, _ any) error {
			return nil
		},
	}, Deps{EventBus: bus})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())

	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish within 5s")
	}

	// Drain events.
	var kinds []string
	for {
		select {
		case e := <-ch:
			if e.Source == events.SourceLoop {
				kinds = append(kinds, e.Kind)
			}
		default:
			goto done
		}
	}
done:

	hasWaitStart := false
	hasSleepStart := false
	for _, k := range kinds {
		if k == events.KindLoopWaitStart {
			hasWaitStart = true
		}
		if k == events.KindLoopSleepStart {
			hasSleepStart = true
		}
	}

	if !hasWaitStart {
		t.Error("missing loop_wait_start event")
	}
	if hasSleepStart {
		t.Error("unexpected loop_sleep_start event for WaitFunc loop")
	}
}

func TestWaitFuncNilPayloadSkipsIteration(t *testing.T) {
	t.Parallel()

	var handlerCalls atomic.Int32
	var waitCalls atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l, err := New(Config{
		Name:    "nil-skip",
		MaxIter: 100, // high limit — we cancel manually
		WaitFunc: func(wCtx context.Context) (any, error) {
			n := waitCalls.Add(1)
			if n <= 3 {
				// First 3 wakes are no-ops (nil payload).
				return nil, nil
			}
			if n == 4 {
				// 4th wake returns a real payload.
				return "real-event", nil
			}
			// Subsequent calls block until cancelled.
			<-wCtx.Done()
			return nil, wCtx.Err()
		},
		Handler: func(_ context.Context, _ any) error {
			handlerCalls.Add(1)
			cancel() // stop after first real iteration
			return nil
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(ctx)
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish within 5s")
	}

	if got := waitCalls.Load(); got < 4 {
		t.Errorf("expected at least 4 wait calls, got %d", got)
	}
	// Handler should only be called once (for the non-nil payload).
	if got := handlerCalls.Load(); got != 1 {
		t.Errorf("expected 1 handler call, got %d", got)
	}

	st := l.Status()
	if st.Iterations != 1 {
		t.Errorf("expected 1 iteration, got %d", st.Iterations)
	}
}

func TestHandlerNoOpSkipsAccounting(t *testing.T) {
	t.Parallel()

	var handlerCalls atomic.Int32
	var waitCalls atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l, err := New(Config{
		Name:    "noop-handler",
		MaxIter: 100,
		WaitFunc: func(wCtx context.Context) (any, error) {
			n := waitCalls.Add(1)
			if n <= 5 {
				return "event", nil
			}
			<-wCtx.Done()
			return nil, wCtx.Err()
		},
		Handler: func(_ context.Context, _ any) error {
			n := handlerCalls.Add(1)
			if n <= 3 {
				// First 3 calls return no-op.
				return ErrNoOp
			}
			if n == 4 {
				// 4th call succeeds.
				return nil
			}
			// 5th call succeeds and stops.
			cancel()
			return nil
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(ctx)
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish within 5s")
	}

	st := l.Status()
	// Only the 2 non-no-op handler calls should be counted.
	if st.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", st.Iterations)
	}
	if st.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", st.Attempts)
	}
}

func TestHandlerNoOpNotCountedAsError(t *testing.T) {
	t.Parallel()

	var waitCalls atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l, err := New(Config{
		Name:    "noop-no-error",
		MaxIter: 100,
		WaitFunc: func(wCtx context.Context) (any, error) {
			if waitCalls.Add(1) > 1 {
				<-wCtx.Done()
				return nil, wCtx.Err()
			}
			return "event", nil
		},
		Handler: func(_ context.Context, _ any) error {
			cancel()
			return ErrNoOp
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(ctx)
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish within 5s")
	}

	st := l.Status()
	if st.ConsecutiveErrors != 0 {
		t.Errorf("ConsecutiveErrors = %d, want 0", st.ConsecutiveErrors)
	}
	if st.LastError != "" {
		t.Errorf("LastError = %q, want empty", st.LastError)
	}
}

func TestHandlerPostIterate(t *testing.T) {
	t.Parallel()

	var results []IterationResult
	var mu sync.Mutex

	l, err := New(Config{
		Name:         "handler-post",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      2,
		Handler: func(_ context.Context, _ any) error {
			return nil
		},
		PostIterate: func(_ context.Context, result IterationResult) error {
			mu.Lock()
			results = append(results, result)
			mu.Unlock()
			return nil
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())
	<-l.Done()

	mu.Lock()
	defer mu.Unlock()

	if len(results) != 2 {
		t.Fatalf("PostIterate called %d times, want 2", len(results))
	}
	for i, r := range results {
		if r.ConvID == "" {
			t.Errorf("result[%d].ConvID is empty", i)
		}
		if r.Model != "" {
			t.Errorf("result[%d].Model = %q, want empty (handler-only)", i, r.Model)
		}
		if r.InputTokens != 0 {
			t.Errorf("result[%d].InputTokens = %d, want 0", i, r.InputTokens)
		}
		if r.Elapsed <= 0 {
			t.Errorf("result[%d].Elapsed = %v, want positive", i, r.Elapsed)
		}
	}
}

func TestHandlerSummary(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name:         "handler-summary",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      2,
		Handler: func(ctx context.Context, _ any) error {
			summary := IterationSummary(ctx)
			if summary != nil {
				summary["devices_located"] = 5
				summary["rooms_updated"] = 2
			}
			return nil
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())
	<-l.Done()

	status := l.Status()
	if len(status.RecentIterations) != 2 {
		t.Fatalf("RecentIterations = %d, want 2", len(status.RecentIterations))
	}

	// Most recent iteration is first (ring buffer prepends).
	for i, snap := range status.RecentIterations {
		if snap.Summary == nil {
			t.Fatalf("RecentIterations[%d].Summary is nil", i)
		}
		if snap.Summary["devices_located"] != 5 {
			t.Errorf("RecentIterations[%d].Summary[devices_located] = %v, want 5", i, snap.Summary["devices_located"])
		}
		if snap.Summary["rooms_updated"] != 2 {
			t.Errorf("RecentIterations[%d].Summary[rooms_updated] = %v, want 2", i, snap.Summary["rooms_updated"])
		}
	}
}

// progressRunner calls OnProgress with a KindLoopLLMStart event, then
// blocks until released so the test can inspect Status() mid-iteration.
type progressRunner struct {
	gate chan struct{} // close to let Run return
}

func (r *progressRunner) Run(_ context.Context, req RunRequest, _ StreamCallback) (*RunResponse, error) {
	if req.OnProgress != nil {
		req.OnProgress(events.KindLoopLLMStart, map[string]any{
			"model":           "test-model",
			"est_tokens":      12345,
			"messages":        3,
			"tools":           7,
			"effective_tools": []string{"alpha_tool", "beta_tool"},
			"active_tags":     []string{"forge", "memory"},
			"complexity":      "moderate",
			"intent":          "check_status",
		})
	}
	<-r.gate
	return &RunResponse{
		Content:        "ok",
		Model:          "test-model",
		RequestID:      "req-live",
		InputTokens:    100,
		OutputTokens:   20,
		EffectiveTools: []string{"alpha_tool", "beta_tool"},
		ActiveTags:     []string{"forge", "memory"},
	}, nil
}

func TestLLMContextInStatus(t *testing.T) {
	t.Parallel()

	bus := events.New()
	gate := make(chan struct{})
	runner := &progressRunner{gate: gate}

	l, err := New(Config{
		Name:         "llm-ctx-test",
		Task:         "test",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
	}, Deps{Runner: runner, EventBus: bus})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	_ = l.Start(ctx)

	// Wait until the loop is processing (runner is blocked on gate).
	deadline := time.After(5 * time.Second)
	for l.Status().State != StateProcessing {
		select {
		case <-deadline:
			t.Fatal("loop never entered processing state")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Give the progress callback a moment to fire.
	time.Sleep(50 * time.Millisecond)

	// Status should include LLM context while processing.
	status := l.Status()
	if status.LLMContext == nil {
		t.Fatal("Status().LLMContext is nil during processing")
	}
	if status.LLMContext["model"] != "test-model" {
		t.Errorf("LLMContext[model] = %v, want test-model", status.LLMContext["model"])
	}
	if status.LLMContext["est_tokens"] != 12345 {
		t.Errorf("LLMContext[est_tokens] = %v, want 12345", status.LLMContext["est_tokens"])
	}
	if status.LLMContext["messages"] != 3 {
		t.Errorf("LLMContext[messages] = %v, want 3", status.LLMContext["messages"])
	}
	if status.LLMContext["complexity"] != "moderate" {
		t.Errorf("LLMContext[complexity] = %v, want moderate", status.LLMContext["complexity"])
	}
	if got, ok := status.LLMContext["effective_tools"].([]string); !ok || len(got) != 2 || got[0] != "alpha_tool" || got[1] != "beta_tool" {
		t.Errorf("LLMContext[effective_tools] = %#v, want [alpha_tool beta_tool]", status.LLMContext["effective_tools"])
	}
	if got, ok := status.LLMContext["active_tags"].([]string); !ok || len(got) != 2 || got[0] != "forge" || got[1] != "memory" {
		t.Errorf("LLMContext[active_tags] = %#v, want [forge memory]", status.LLMContext["active_tags"])
	}

	// LLMContext should not contain loop infrastructure keys.
	if _, ok := status.LLMContext["loop_id"]; ok {
		t.Error("LLMContext should not contain loop_id")
	}

	// Release the runner so the iteration completes.
	close(gate)
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish")
	}

	// After iteration completes, LLMContext should be cleared.
	status = l.Status()
	if status.LLMContext != nil {
		t.Errorf("LLMContext should be nil after iteration, got %v", status.LLMContext)
	}

	if len(status.RecentIterations) != 1 {
		t.Fatalf("RecentIterations = %d, want 1", len(status.RecentIterations))
	}
	snap := status.RecentIterations[0]
	if len(snap.EffectiveTools) != 2 || snap.EffectiveTools[0] != "alpha_tool" || snap.EffectiveTools[1] != "beta_tool" {
		t.Errorf("RecentIterations[0].EffectiveTools = %v, want [alpha_tool beta_tool]", snap.EffectiveTools)
	}
	if len(snap.ActiveTags) != 2 || snap.ActiveTags[0] != "forge" || snap.ActiveTags[1] != "memory" {
		t.Errorf("RecentIterations[0].ActiveTags = %v, want [forge memory]", snap.ActiveTags)
	}
}

func TestActiveTagsInStatus(t *testing.T) {
	t.Parallel()

	bus := events.New()
	l, err := New(Config{
		Name:    "active-tags-test",
		Handler: func(context.Context, any) error { return nil },
	}, Deps{EventBus: bus})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Without SetActiveTagsFunc, ActiveTags should be nil.
	status := l.Status()
	if status.ActiveTags != nil {
		t.Errorf("ActiveTags should be nil without callback, got %v", status.ActiveTags)
	}

	// With a callback, ActiveTags should reflect the callback's return.
	l.SetActiveTagsFunc(func() []string {
		return []string{"forge", "memory"}
	})

	status = l.Status()
	if len(status.ActiveTags) != 2 {
		t.Fatalf("ActiveTags length = %d, want 2", len(status.ActiveTags))
	}
	if status.ActiveTags[0] != "forge" || status.ActiveTags[1] != "memory" {
		t.Errorf("ActiveTags = %v, want [forge memory]", status.ActiveTags)
	}

	// Callback returning nil should result in nil ActiveTags.
	l.SetActiveTagsFunc(func() []string { return nil })
	status = l.Status()
	if status.ActiveTags != nil {
		t.Errorf("ActiveTags should be nil when callback returns nil, got %v", status.ActiveTags)
	}
}

func TestHandlerReportedToolingStaysInRecentIterationsOnly(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ch := bus.Subscribe(16)
	defer bus.Unsubscribe(ch)

	l, err := New(Config{
		Name: "handler-tooling-test",
		Handler: func(ctx context.Context, _ any) error {
			ReportAgentRun(ctx, AgentRunSummary{
				RequestID:      "req-handler",
				Model:          "test-model",
				InputTokens:    42,
				OutputTokens:   9,
				ContextWindow:  200000,
				ToolsUsed:      map[string]int{"archive_search": 2, "remember_fact": 1},
				ActiveTags:     []string{"forge", "ha"},
				EffectiveTools: []string{"forge_issue_list", "get_state"},
				LoadedCapabilities: []toolcatalog.LoadedCapabilityEntry{
					{Tag: "forge", Description: "Forge tools", ToolCount: 8},
					{Tag: "ha", Description: "Home Assistant tools", ToolCount: 5},
				},
			})
			return nil
		},
		SleepMin:     1 * time.Millisecond,
		SleepMax:     1 * time.Millisecond,
		SleepDefault: 1 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
	}, Deps{EventBus: bus})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish")
	}

	status := l.Status()
	if len(status.RecentIterations) != 1 {
		t.Fatalf("RecentIterations = %d, want 1", len(status.RecentIterations))
	}
	snap := status.RecentIterations[0]
	if snap.RequestID != "req-handler" {
		t.Fatalf("RequestID = %q, want req-handler", snap.RequestID)
	}
	if !slices.Equal(snap.ActiveTags, []string{"forge", "ha"}) {
		t.Fatalf("snap.ActiveTags = %v, want [forge ha]", snap.ActiveTags)
	}
	if !slices.Equal(snap.EffectiveTools, []string{"forge_issue_list", "get_state"}) {
		t.Fatalf("snap.EffectiveTools = %v, want [forge_issue_list get_state]", snap.EffectiveTools)
	}
	if snap.ToolsUsed["archive_search"] != 2 || snap.ToolsUsed["remember_fact"] != 1 {
		t.Fatalf("snap.ToolsUsed = %v, want archive_search=2 remember_fact=1", snap.ToolsUsed)
	}
	if snap.ContextWindow != 200000 {
		t.Fatalf("snap.ContextWindow = %d, want 200000", snap.ContextWindow)
	}
	if snap.Tooling.ToolsUsed["archive_search"] != 2 || snap.Tooling.ToolsUsed["remember_fact"] != 1 {
		t.Fatalf("snap.Tooling.ToolsUsed = %v, want archive_search=2 remember_fact=1", snap.Tooling.ToolsUsed)
	}
	if len(snap.Tooling.LoadedCapabilities) != 2 {
		t.Fatalf("snap.Tooling.LoadedCapabilities = %v, want 2 entries", snap.Tooling.LoadedCapabilities)
	}
	if len(status.Tooling.LoadedTags) != 0 {
		t.Fatalf("status.Tooling.LoadedTags = %v, want empty for idle handler loop", status.Tooling.LoadedTags)
	}
	if len(status.Tooling.EffectiveTools) != 0 {
		t.Fatalf("status.Tooling.EffectiveTools = %v, want empty for idle handler loop", status.Tooling.EffectiveTools)
	}
	if len(status.Tooling.LoadedCapabilities) != 0 {
		t.Fatalf("status.Tooling.LoadedCapabilities = %v, want empty for idle handler loop", status.Tooling.LoadedCapabilities)
	}

	var complete *events.Event
	for {
		select {
		case evt := <-ch:
			if evt.Kind == events.KindLoopIterationComplete && evt.Source == events.SourceLoop {
				evtCopy := evt
				complete = &evtCopy
			}
		default:
			goto drained
		}
	}
drained:
	if complete == nil {
		t.Fatal("missing loop_iteration_complete event")
	}
	toolsUsed, ok := complete.Data["tools_used"].(map[string]int)
	if !ok {
		t.Fatalf("event tools_used = %#v, want map[string]int", complete.Data["tools_used"])
	}
	if toolsUsed["archive_search"] != 2 || toolsUsed["remember_fact"] != 1 {
		t.Fatalf("event tools_used = %v, want archive_search=2 remember_fact=1", toolsUsed)
	}
	if complete.Data["context_window"] != 200000 {
		t.Fatalf("event context_window = %v, want 200000", complete.Data["context_window"])
	}
}

// --- Initial sleep tests ---

func TestTimerLoopInitialSleep(t *testing.T) {
	t.Parallel()

	// Use a SleepDefault long enough to measure but short enough for
	// a fast test. Zero jitter so the initial sleep is deterministic.
	const initialDelay = 50 * time.Millisecond

	var firstCallAt time.Time
	var mu sync.Mutex

	l, err := New(Config{
		Name:         "initial-sleep",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     100 * time.Millisecond,
		SleepDefault: initialDelay,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
		Handler: func(_ context.Context, _ any) error {
			mu.Lock()
			firstCallAt = time.Now()
			mu.Unlock()
			return nil
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	startedAt := time.Now()
	_ = l.Start(context.Background())
	<-l.Done()

	mu.Lock()
	elapsed := firstCallAt.Sub(startedAt)
	mu.Unlock()

	// The first iteration should not fire until after the initial
	// sleep. Allow some slack for scheduling but it should be at
	// least 80% of the configured delay.
	minExpected := initialDelay * 80 / 100
	if elapsed < minExpected {
		t.Errorf("first iteration fired after %v, want at least %v (initial sleep = %v)",
			elapsed, minExpected, initialDelay)
	}
}

func TestTimerLoopInitialSleep_Cancelled(t *testing.T) {
	t.Parallel()

	// Verify the loop stops cleanly if context is cancelled during
	// initial sleep (no iteration should run).
	var calls atomic.Int32

	l, err := New(Config{
		Name:         "initial-cancel",
		SleepMin:     1 * time.Second,
		SleepMax:     5 * time.Second,
		SleepDefault: 5 * time.Second,
		Jitter:       Float64Ptr(0),
		Handler: func(_ context.Context, _ any) error {
			calls.Add(1)
			return nil
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	_ = l.Start(ctx)

	// Cancel quickly — before initial sleep finishes.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-l.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not stop after context cancellation during initial sleep")
	}

	if calls.Load() != 0 {
		t.Errorf("handler was called %d times, want 0 (cancelled during initial sleep)", calls.Load())
	}

	status := l.Status()
	if status.State != StateStopped {
		t.Errorf("state = %v, want Stopped", status.State)
	}
}

func TestTimerLoopInitialSleep_PublishesEvent(t *testing.T) {
	t.Parallel()

	bus := events.New()
	sub := bus.Subscribe(16)
	defer bus.Unsubscribe(sub)

	l, err := New(Config{
		Name:         "initial-event",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     10 * time.Millisecond,
		SleepDefault: 5 * time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
		Handler: func(_ context.Context, _ any) error {
			return nil
		},
	}, Deps{EventBus: bus})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())
	<-l.Done()

	// Collect events. The initial sleep should produce a sleep_start
	// event with initial=true.
	var foundInitial bool
	timeout := time.After(1 * time.Second)
	for !foundInitial {
		select {
		case ev := <-sub:
			if ev.Kind == events.KindLoopSleepStart {
				if initial, ok := ev.Data["initial"].(bool); ok && initial {
					foundInitial = true
				}
			}
		case <-timeout:
			t.Fatal("timed out waiting for initial sleep event")
		}
	}
}

func TestWaitFuncLoopNoInitialSleep(t *testing.T) {
	t.Parallel()

	// Event-driven loops should NOT have initial sleep — they block
	// on WaitFunc immediately.
	var firstCallAt time.Time
	var mu sync.Mutex

	eventCh := make(chan struct{}, 1)
	eventCh <- struct{}{} // Pre-load one event so it fires immediately.

	l, err := New(Config{
		Name:         "waitfunc-no-initial",
		SleepMin:     1 * time.Millisecond,
		SleepMax:     100 * time.Millisecond,
		SleepDefault: 100 * time.Millisecond,
		MaxIter:      1,
		WaitFunc: func(ctx context.Context) (any, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-eventCh:
				return "event", nil
			}
		},
		Handler: func(_ context.Context, _ any) error {
			mu.Lock()
			firstCallAt = time.Now()
			mu.Unlock()
			return nil
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	startedAt := time.Now()
	_ = l.Start(context.Background())
	<-l.Done()

	mu.Lock()
	elapsed := firstCallAt.Sub(startedAt)
	mu.Unlock()

	// Event-driven loop should fire quickly — well under SleepDefault.
	// Use a fraction of SleepDefault rather than a hard-coded bound
	// so the assertion stays meaningful under scheduler load.
	maxElapsed := 100 * time.Millisecond / 2 // 50% of SleepDefault
	if elapsed > maxElapsed {
		t.Errorf("event-driven loop took %v to first iteration, want < %v (SleepDefault = %v)",
			elapsed, maxElapsed, 100*time.Millisecond)
	}
}
