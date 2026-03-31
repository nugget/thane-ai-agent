package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestStartWorkers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		workers []struct {
			name string
			fn   func(ctx context.Context) error
		}
		wantErr    bool
		wantCalls  int // how many workers should have been called
		errContain string
	}{
		{
			name:      "no workers",
			workers:   nil,
			wantCalls: 0,
		},
		{
			name: "all succeed",
			workers: []struct {
				name string
				fn   func(ctx context.Context) error
			}{
				{"alpha", func(context.Context) error { return nil }},
				{"beta", func(context.Context) error { return nil }},
				{"gamma", func(context.Context) error { return nil }},
			},
			wantCalls: 3,
		},
		{
			name: "second fails stops early",
			workers: []struct {
				name string
				fn   func(ctx context.Context) error
			}{
				{"alpha", func(context.Context) error { return nil }},
				{"beta", func(context.Context) error { return errors.New("boom") }},
				{"gamma", func(context.Context) error { return nil }},
			},
			wantErr:    true,
			wantCalls:  2,
			errContain: `"beta"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			called := 0
			a := &App{}
			for _, w := range tt.workers {
				w := w // capture
				a.deferWorker(w.name, func(ctx context.Context) error {
					called++
					return w.fn(ctx)
				})
			}

			err := a.StartWorkers(context.Background())

			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.errContain != "" && err != nil {
				if got := err.Error(); !strings.Contains(got, tt.errContain) {
					t.Errorf("error %q does not contain %q", got, tt.errContain)
				}
			}
			if called != tt.wantCalls {
				t.Errorf("called %d workers, want %d", called, tt.wantCalls)
			}

			// After successful StartWorkers, pendingWorkers should be nil.
			if !tt.wantErr && a.pendingWorkers != nil {
				t.Error("pendingWorkers not cleared after successful StartWorkers")
			}
		})
	}
}

func TestCloserStack_LIFO_order(t *testing.T) {
	t.Parallel()

	var order []string
	a := &App{logger: slog.Default()}
	a.onClose("first", func() { order = append(order, "first") })
	a.onClose("second", func() { order = append(order, "second") })
	a.onClose("third", func() { order = append(order, "third") })

	a.shutdown()

	want := []string{"third", "second", "first"}
	if len(order) != len(want) {
		t.Fatalf("got %d closers, want %d", len(order), len(want))
	}
	for i, name := range want {
		if order[i] != name {
			t.Errorf("closer[%d] = %q, want %q", i, order[i], name)
		}
	}

	// Verify the stack was cleared.
	if a.closers != nil {
		t.Error("closers not cleared after shutdown")
	}
}

func TestCloserStack_onCloseErr_logs_errors(t *testing.T) {
	t.Parallel()

	var order []string
	a := &App{logger: slog.Default()}
	a.onCloseErr("failing", func() error {
		order = append(order, "failing")
		return errors.New("close error")
	})
	a.onClose("ok", func() { order = append(order, "ok") })

	// Should not panic, even if the closer returns an error.
	a.shutdown()

	want := []string{"ok", "failing"}
	if len(order) != len(want) {
		t.Fatalf("got %d closers, want %d", len(order), len(want))
	}
	for i, name := range want {
		if order[i] != name {
			t.Errorf("closer[%d] = %q, want %q", i, order[i], name)
		}
	}
}

func TestStartWorkers_registers_closers(t *testing.T) {
	t.Parallel()

	var order []string
	a := &App{logger: slog.Default()}

	// Simulate a resource registered during New().
	a.onClose("resource", func() { order = append(order, "resource") })

	// Worker registers its own closer after starting.
	a.deferWorker("worker", func(ctx context.Context) error {
		a.onClose("worker-stop", func() { order = append(order, "worker-stop") })
		return nil
	})

	if err := a.StartWorkers(context.Background()); err != nil {
		t.Fatal(err)
	}

	a.shutdown()

	// LIFO: worker-stop (registered last) runs before resource (registered first).
	want := []string{"worker-stop", "resource"}
	if len(order) != len(want) {
		t.Fatalf("got %v, want %v", order, want)
	}
	for i, name := range want {
		if order[i] != name {
			t.Errorf("closer[%d] = %q, want %q", i, order[i], name)
		}
	}
}

func TestStartWorkers_subsequent_call_is_noop(t *testing.T) {
	t.Parallel()

	called := 0
	a := &App{}
	a.deferWorker("once", func(context.Context) error {
		called++
		return nil
	})

	if err := a.StartWorkers(context.Background()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := a.StartWorkers(context.Background()); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if called != 1 {
		t.Errorf("worker called %d times, want 1", called)
	}
}
