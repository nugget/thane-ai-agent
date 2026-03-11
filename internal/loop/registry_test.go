package loop

import (
	"context"
	"testing"
	"time"
)

// mustNew is a test helper that calls New and fails on error.
func mustNew(t *testing.T, cfg Config, deps Deps) *Loop {
	t.Helper()
	l, err := New(cfg, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

func TestRegistryRegisterDeregister(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	l := mustNew(t, Config{Name: "test-loop"}, Deps{Runner: &noopRunner{}})

	if err := r.Register(l); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if r.ActiveCount() != 1 {
		t.Errorf("ActiveCount = %d, want 1", r.ActiveCount())
	}

	// Duplicate registration fails.
	if err := r.Register(l); err == nil {
		t.Error("duplicate Register should fail")
	}

	r.Deregister(l.ID())
	if r.ActiveCount() != 0 {
		t.Errorf("ActiveCount after deregister = %d, want 0", r.ActiveCount())
	}

	// Deregister of unknown ID is a no-op.
	r.Deregister("nonexistent")
}

func TestRegistryConcurrencyLimit(t *testing.T) {
	t.Parallel()

	r := NewRegistry(WithMaxLoops(2))
	runner := &noopRunner{}

	l1 := mustNew(t, Config{Name: "loop-1"}, Deps{Runner: runner})
	l2 := mustNew(t, Config{Name: "loop-2"}, Deps{Runner: runner})
	l3 := mustNew(t, Config{Name: "loop-3"}, Deps{Runner: runner})

	if err := r.Register(l1); err != nil {
		t.Fatalf("Register l1: %v", err)
	}
	if err := r.Register(l2); err != nil {
		t.Fatalf("Register l2: %v", err)
	}
	if err := r.Register(l3); err == nil {
		t.Error("Register l3 should fail at concurrency limit")
	}

	// After deregistering one, we can add another.
	r.Deregister(l1.ID())
	if err := r.Register(l3); err != nil {
		t.Fatalf("Register l3 after deregister: %v", err)
	}
}

func TestRegistryGetAndGetByName(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	l := mustNew(t, Config{Name: "named-loop"}, Deps{Runner: &noopRunner{}})

	if err := r.Register(l); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if got := r.Get(l.ID()); got != l {
		t.Errorf("Get(%q) returned wrong loop", l.ID())
	}
	if got := r.Get("nonexistent"); got != nil {
		t.Error("Get(nonexistent) should return nil")
	}

	if got := r.GetByName("named-loop"); got != l {
		t.Error("GetByName(named-loop) returned wrong loop")
	}
	if got := r.GetByName("missing"); got != nil {
		t.Error("GetByName(missing) should return nil")
	}
}

func TestRegistryList(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	runner := &noopRunner{}
	l1 := mustNew(t, Config{Name: "bravo"}, Deps{Runner: runner})
	l2 := mustNew(t, Config{Name: "alpha"}, Deps{Runner: runner})

	_ = r.Register(l1)
	_ = r.Register(l2)

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	// Should be sorted by name.
	if list[0].Name() != "alpha" {
		t.Errorf("List[0].Name = %q, want alpha", list[0].Name())
	}
	if list[1].Name() != "bravo" {
		t.Errorf("List[1].Name = %q, want bravo", list[1].Name())
	}
}

func TestRegistryStatuses(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	l := mustNew(t, Config{Name: "status-test"}, Deps{Runner: &noopRunner{}})
	_ = r.Register(l)

	statuses := r.Statuses()
	if len(statuses) != 1 {
		t.Fatalf("Statuses len = %d, want 1", len(statuses))
	}
	if statuses[0].Name != "status-test" {
		t.Errorf("Status.Name = %q, want status-test", statuses[0].Name)
	}
	if statuses[0].State != StatePending {
		t.Errorf("Status.State = %q, want %q", statuses[0].State, StatePending)
	}
}

func TestRegistryShutdownAll(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	runner := &noopRunner{}

	l1 := mustNew(t, Config{
		Name:     "shutdown-1",
		Task:     "test",
		SleepMin: time.Hour,
		SleepMax: time.Hour,
	}, Deps{Runner: runner})
	l2 := mustNew(t, Config{
		Name:     "shutdown-2",
		Task:     "test",
		SleepMin: time.Hour,
		SleepMax: time.Hour,
	}, Deps{Runner: runner})

	_ = r.Register(l1)
	_ = r.Register(l2)

	ctx := context.Background()
	_ = l1.Start(ctx)
	_ = l2.Start(ctx)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	stopped := r.ShutdownAll(shutdownCtx)
	if stopped != 2 {
		t.Errorf("ShutdownAll stopped %d loops, want 2", stopped)
	}
	if r.ActiveCount() != 0 {
		t.Errorf("ActiveCount after shutdown = %d, want 0", r.ActiveCount())
	}
}

func TestRegistryShutdownAllWithUnstartedLoops(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	runner := &noopRunner{}

	// Register but don't start.
	l := mustNew(t, Config{Name: "unstarted"}, Deps{Runner: runner})
	_ = r.Register(l)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stopped := r.ShutdownAll(shutdownCtx)
	if stopped != 1 {
		t.Errorf("ShutdownAll stopped %d, want 1 (unstarted loops should be drained immediately)", stopped)
	}
	if r.ActiveCount() != 0 {
		t.Errorf("ActiveCount = %d, want 0", r.ActiveCount())
	}
}

// noopRunner is a mock Runner that returns immediately with minimal
// data. Used for lifecycle tests where the LLM response doesn't matter.
type noopRunner struct{}

func (r *noopRunner) Run(_ context.Context, _ RunRequest, _ StreamCallback) (*RunResponse, error) {
	return &RunResponse{
		Content:      "ok",
		Model:        "test-model",
		InputTokens:  10,
		OutputTokens: 5,
	}, nil
}
