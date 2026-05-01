package messages

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestActivityIndicatorBeginEnd(t *testing.T) {
	var starts atomic.Int32
	var refreshes atomic.Int32
	var stops atomic.Int32
	refreshed := make(chan struct{}, 1)

	indicator := ActivityIndicator{
		Name:     "test",
		Interval: time.Millisecond,
		Start: func(context.Context) error {
			starts.Add(1)
			return nil
		},
		Refresh: func(context.Context) error {
			refreshes.Add(1)
			select {
			case refreshed <- struct{}{}:
			default:
			}
			return nil
		},
		Stop: func(context.Context) error {
			stops.Add(1)
			return nil
		},
	}

	cancel := indicator.Begin(context.Background())
	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for refresh")
	}
	cancel()
	indicator.End(context.Background())

	if starts.Load() != 1 {
		t.Fatalf("starts = %d, want 1", starts.Load())
	}
	if refreshes.Load() == 0 {
		t.Fatal("expected at least one refresh")
	}
	if stops.Load() != 1 {
		t.Fatalf("stops = %d, want 1", stops.Load())
	}
}
