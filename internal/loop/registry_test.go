package loop

import (
	"context"
	"testing"
	"time"
)

func TestRegistryRegisterDeregister(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	l := New(Config{Name: "test-loop"}, Deps{})

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

	l1 := New(Config{Name: "loop-1"}, Deps{})
	l2 := New(Config{Name: "loop-2"}, Deps{})
	l3 := New(Config{Name: "loop-3"}, Deps{})

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
	l := New(Config{Name: "named-loop"}, Deps{})

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
	l1 := New(Config{Name: "bravo"}, Deps{})
	l2 := New(Config{Name: "alpha"}, Deps{})

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
	l := New(Config{Name: "status-test"}, Deps{})
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

	// Create a mock runner that blocks until context is cancelled.
	runner := &blockingRunner{}

	l1 := New(Config{
		Name:     "shutdown-1",
		Task:     "test",
		SleepMin: time.Hour, // won't actually sleep this long
		SleepMax: time.Hour,
	}, Deps{Runner: runner})
	l2 := New(Config{
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stopped := r.ShutdownAll(shutdownCtx)
	if stopped != 2 {
		t.Errorf("ShutdownAll stopped %d loops, want 2", stopped)
	}
	if r.ActiveCount() != 0 {
		t.Errorf("ActiveCount after shutdown = %d, want 0", r.ActiveCount())
	}
}

// blockingRunner is a mock Runner that returns immediately with minimal
// data. Used for lifecycle tests where the LLM response doesn't matter.
type blockingRunner struct{}

func (r *blockingRunner) Run(ctx context.Context, req RunRequest, _ StreamCallback) (*RunResponse, error) {
	// Return immediately so the loop proceeds to sleep.
	return &RunResponse{
		Content:      "ok",
		Model:        "test-model",
		InputTokens:  10,
		OutputTokens: 5,
	}, nil
}
