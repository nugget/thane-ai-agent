package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestStdioTransport_AcquireRespectsContext(t *testing.T) {
	tr := NewStdioTransport(StdioConfig{Command: "echo"})

	// Pre-fill the semaphore to simulate another goroutine holding it.
	tr.sem <- struct{}{}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := tr.acquire(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("acquire() = %v, want context.DeadlineExceeded", err)
	}
}

func TestStdioTransport_AcquireSuccess(t *testing.T) {
	tr := NewStdioTransport(StdioConfig{Command: "echo"})

	ctx := context.Background()
	if err := tr.acquire(ctx); err != nil {
		t.Fatalf("acquire() = %v, want nil", err)
	}
	tr.release()
}

func TestStdioTransport_AcquireAlreadyCancelled(t *testing.T) {
	tr := NewStdioTransport(StdioConfig{Command: "echo"})

	// Pre-fill semaphore.
	tr.sem <- struct{}{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel before acquire.

	err := tr.acquire(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("acquire() = %v, want context.Canceled", err)
	}
}

func TestStdioTransport_AcquireAlreadyCancelledSemaphoreFree(t *testing.T) {
	tr := NewStdioTransport(StdioConfig{Command: "echo"})

	// Cancel the context before attempting to acquire with a free semaphore.
	// The post-acquire double-check must catch this and release the token.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := tr.acquire(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire() with cancelled context = %v, want context.Canceled", err)
	}

	// Verify the semaphore was not left held.
	select {
	case <-tr.sem:
		t.Fatal("semaphore was acquired despite cancelled context")
	default:
		// OK: semaphore is free.
	}
}

func TestStdioTransport_ReleaseFreesSlot(t *testing.T) {
	tr := NewStdioTransport(StdioConfig{Command: "echo"})

	ctx := context.Background()

	// First acquire.
	if err := tr.acquire(ctx); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// Release.
	tr.release()

	// Second acquire should succeed without blocking.
	if err := tr.acquire(ctx); err != nil {
		t.Fatalf("second acquire after release: %v", err)
	}
	tr.release()
}

func TestStdioTransport_ConcurrentAcquireTimeout(t *testing.T) {
	tr := NewStdioTransport(StdioConfig{Command: "echo"})

	ctx := context.Background()
	if err := tr.acquire(ctx); err != nil {
		t.Fatalf("initial acquire: %v", err)
	}

	// Second goroutine tries to acquire with a short timeout.
	shortCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	var acquireErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		acquireErr = tr.acquire(shortCtx)
	}()

	wg.Wait()

	if !errors.Is(acquireErr, context.DeadlineExceeded) {
		t.Errorf("concurrent acquire = %v, want context.DeadlineExceeded", acquireErr)
	}

	// Release the original hold — transport is still usable.
	tr.release()

	// Subsequent acquire should work.
	if err := tr.acquire(ctx); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	tr.release()
}

func TestStdioTransport_SendReturnsErrWhenSemaphoreBusy(t *testing.T) {
	tr := NewStdioTransport(StdioConfig{Command: "echo"})

	// Hold the semaphore to simulate a long-running operation.
	tr.sem <- struct{}{}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := tr.Send(ctx, &Request{
		JSONRPC: "2.0",
		ID:      99,
		Method:  "ping",
	})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Send() = %v, want context.DeadlineExceeded", err)
	}
}

func TestStdioTransport_NotifyReturnsErrWhenSemaphoreBusy(t *testing.T) {
	tr := NewStdioTransport(StdioConfig{Command: "echo"})

	// Hold the semaphore.
	tr.sem <- struct{}{}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := tr.Notify(ctx, &Notification{
		JSONRPC: "2.0",
		Method:  "notifications/test",
	})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Notify() = %v, want context.DeadlineExceeded", err)
	}
}

func TestStdioTransport_CloseBlocksUntilSemaphoreAvailable(t *testing.T) {
	tr := NewStdioTransport(StdioConfig{Command: "echo"})

	// Acquire the semaphore.
	ctx := context.Background()
	if err := tr.acquire(ctx); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- tr.Close()
	}()

	// Close should be blocked.
	select {
	case <-closeDone:
		t.Fatal("Close() returned before semaphore was released")
	case <-time.After(200 * time.Millisecond):
		// Expected: Close is blocked.
	}

	// Release semaphore — Close should proceed.
	tr.release()

	select {
	case err := <-closeDone:
		// Close on an unstarted transport returns nil.
		if err != nil {
			t.Errorf("Close() = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close() did not return after semaphore release")
	}
}
