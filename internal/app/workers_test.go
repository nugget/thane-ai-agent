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
		name       string
		workers    []func(ctx context.Context) error
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
			workers: []func(ctx context.Context) error{
				func(context.Context) error { return nil },
				func(context.Context) error { return nil },
				func(context.Context) error { return nil },
			},
			wantCalls: 3,
		},
		{
			name: "second fails stops early",
			workers: []func(ctx context.Context) error{
				func(context.Context) error { return nil },
				func(context.Context) error { return errors.New("boom") },
				func(context.Context) error { return nil },
			},
			wantErr:    true,
			wantCalls:  2,
			errContain: "boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			called := 0
			a := &App{}
			for _, fn := range tt.workers {
				fn := fn // capture
				a.deferWorker(func(ctx context.Context) error {
					called++
					return fn(ctx)
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
