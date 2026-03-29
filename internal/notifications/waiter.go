package notifications

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// EscalationResponse holds the result of a synchronous escalation.
type EscalationResponse struct {
	ActionID string // the action chosen by the human
	TimedOut bool   // true if the timeout expired without a response
}

// ResponseWaiter provides synchronous blocking for escalation tools.
// When a tool sends an actionable notification, it registers a waiter.
// When the callback fires (MQTT or resolve_actionable), it signals the
// waiter with the chosen action.
type ResponseWaiter struct {
	mu      sync.Mutex
	waiters map[string]chan EscalationResponse
}

// NewResponseWaiter creates a waiter registry.
func NewResponseWaiter() *ResponseWaiter {
	return &ResponseWaiter{
		waiters: make(map[string]chan EscalationResponse),
	}
}

// Register creates a waiter channel for the given request ID. The
// returned channel receives the response when Signal resolves the
// callback or the timeout expires.
func (w *ResponseWaiter) Register(requestID string) <-chan EscalationResponse {
	ch := make(chan EscalationResponse, 1)
	w.mu.Lock()
	w.waiters[requestID] = ch
	w.mu.Unlock()
	return ch
}

// Signal delivers a response to a waiting escalation tool. Returns
// true if a waiter was found, false if the request has no waiter
// (e.g., the escalation timed out and was cleaned up, or it was a
// non-synchronous notification).
func (w *ResponseWaiter) Signal(requestID, actionID string) bool {
	w.mu.Lock()
	ch, ok := w.waiters[requestID]
	if ok {
		delete(w.waiters, requestID)
	}
	w.mu.Unlock()
	if !ok {
		return false
	}
	ch <- EscalationResponse{ActionID: actionID}
	return true
}

// SignalTimeout delivers a timeout response to a waiting escalation.
func (w *ResponseWaiter) SignalTimeout(requestID string) bool {
	w.mu.Lock()
	ch, ok := w.waiters[requestID]
	if ok {
		delete(w.waiters, requestID)
	}
	w.mu.Unlock()
	if !ok {
		return false
	}
	ch <- EscalationResponse{TimedOut: true}
	return true
}

// Wait blocks until the response arrives or the context is cancelled.
// Returns an error only when the parent context is cancelled (e.g.,
// the run is shutting down). Cleans up the waiter on exit.
func (w *ResponseWaiter) Wait(ctx context.Context, requestID string, ch <-chan EscalationResponse) (EscalationResponse, error) {
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		w.mu.Lock()
		delete(w.waiters, requestID)
		w.mu.Unlock()
		return EscalationResponse{}, fmt.Errorf("escalation %s: %w", requestID, ctx.Err())
	}
}

// WaitWithTimeout blocks until the response arrives, the timeout
// expires, or the parent context is cancelled. A timeout expiry
// returns an [EscalationResponse] with TimedOut=true (not an error),
// since timeouts are an expected outcome. Only parent context
// cancellation (e.g., run shutdown) returns an error.
func (w *ResponseWaiter) WaitWithTimeout(ctx context.Context, requestID string, ch <-chan EscalationResponse, timeout time.Duration) (EscalationResponse, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-ch:
		return resp, nil
	case <-timer.C:
		// Local timeout expired — this is the expected timeout path.
		// Clean up the waiter so the TimeoutWatcher doesn't also signal.
		w.mu.Lock()
		delete(w.waiters, requestID)
		w.mu.Unlock()
		return EscalationResponse{TimedOut: true}, nil
	case <-ctx.Done():
		// Parent context cancelled — run is shutting down.
		w.mu.Lock()
		delete(w.waiters, requestID)
		w.mu.Unlock()
		return EscalationResponse{}, fmt.Errorf("escalation %s: %w", requestID, ctx.Err())
	}
}
