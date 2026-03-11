package loop

import (
	"context"
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

func TestStopBeforeStartIsNoop(t *testing.T) {
	t.Parallel()

	l, err := New(Config{Name: "never-started"}, Deps{Runner: &blockingRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	l.Stop() // should not panic

	if l.Done() != nil {
		t.Error("Done() should be nil before Start")
	}
}

func TestNewRequiresRunner(t *testing.T) {
	t.Parallel()

	_, err := New(Config{Name: "no-runner"}, Deps{})
	if err != ErrNilRunner {
		t.Errorf("New without runner: got %v, want ErrNilRunner", err)
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
