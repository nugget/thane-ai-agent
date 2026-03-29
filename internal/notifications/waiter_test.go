package notifications

import (
	"context"
	"testing"
	"time"
)

func TestResponseWaiter_Signal(t *testing.T) {
	t.Parallel()
	w := NewResponseWaiter()

	ch := w.Register("req-1")

	// Signal in a goroutine.
	go func() {
		time.Sleep(10 * time.Millisecond)
		w.Signal("req-1", "approve")
	}()

	resp, err := w.Wait(context.Background(), "req-1", ch)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ActionID != "approve" {
		t.Errorf("ActionID = %q, want approve", resp.ActionID)
	}
	if resp.TimedOut {
		t.Error("should not be timed out")
	}
}

func TestResponseWaiter_SignalTimeout(t *testing.T) {
	t.Parallel()
	w := NewResponseWaiter()

	ch := w.Register("req-2")

	go func() {
		time.Sleep(10 * time.Millisecond)
		w.SignalTimeout("req-2")
	}()

	resp, err := w.Wait(context.Background(), "req-2", ch)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.TimedOut {
		t.Error("should be timed out")
	}
}

func TestResponseWaiter_ContextCancellation(t *testing.T) {
	t.Parallel()
	w := NewResponseWaiter()

	ch := w.Register("req-3")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := w.Wait(ctx, "req-3", ch)
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestResponseWaiter_SignalNoWaiter(t *testing.T) {
	t.Parallel()
	w := NewResponseWaiter()

	// Signal with no registered waiter should return false.
	if w.Signal("nonexistent", "approve") {
		t.Error("Signal should return false for unregistered request")
	}
}

func TestResponseWaiter_WaitWithTimeout(t *testing.T) {
	t.Parallel()
	w := NewResponseWaiter()

	ch := w.Register("req-4")

	// Don't signal — let the local timeout expire.
	// This should return a TimedOut response, not an error.
	resp, err := w.WaitWithTimeout(context.Background(), "req-4", ch, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("expected timeout response, got error: %v", err)
	}
	if !resp.TimedOut {
		t.Error("expected TimedOut=true")
	}
}
