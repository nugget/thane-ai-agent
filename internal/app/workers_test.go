package app

import (
	"context"
	"errors"
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
