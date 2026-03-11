package loop

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/events"
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

	t.Run("neither Task nor TaskBuilder fails", func(t *testing.T) {
		t.Parallel()
		_, err := New(Config{Name: "neither"}, Deps{Runner: runner})
		if err == nil {
			t.Error("expected error when neither Task nor TaskBuilder set")
		}
	})
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

// callbackRunner delegates Run to a caller-provided function.
type callbackRunner struct {
	fn func(context.Context, RunRequest) (*RunResponse, error)
}

func (r *callbackRunner) Run(ctx context.Context, req RunRequest, _ StreamCallback) (*RunResponse, error) {
	return r.fn(ctx, req)
}
