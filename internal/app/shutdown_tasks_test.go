package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestShutdownTasksTailCompletesBeforeFinishReturns pins the ordering
// contract that produced "sql: database is closed" at every restart:
// finish (and therefore Serve's return, and therefore the deferred
// store close) must not complete while the shutdown tail is mid-work.
func TestShutdownTasksTailCompletesBeforeFinishReturns(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	tasks := newShutdownTasks(testLogger(), time.Minute)

	entered := make(chan struct{})
	release := make(chan struct{})
	var completed atomic.Bool
	tasks.watch(ctx, func() {
		close(entered)
		<-release
		completed.Store(true)
	})

	cancel()
	<-entered // the tail is now running

	finished := make(chan struct{})
	go func() {
		tasks.finish(false)
		close(finished)
	}()

	select {
	case <-finished:
		t.Fatal("finish returned while the tail was still running")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("finish never returned after the tail completed")
	}
	if !completed.Load() {
		t.Fatal("finish returned without the tail having completed")
	}
}

// TestShutdownTasksFatalAbortSkipsTail pins the fatal-server-error
// path: the watcher is released without running the tail, and a later
// cancel (the command caller's deferred cancel) must not fire the tail
// into stores the caller already closed.
func TestShutdownTasksFatalAbortSkipsTail(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	tasks := newShutdownTasks(testLogger(), time.Minute)

	var ran atomic.Bool
	tasks.watch(ctx, func() { ran.Store(true) })

	tasks.finish(true) // returns once the watcher acknowledges the abort
	cancel()           // the late cancel arrives after Serve returned

	time.Sleep(50 * time.Millisecond)
	if ran.Load() {
		t.Fatal("tail ran despite the fatal abort")
	}
}

// TestShutdownTasksWedgedTailDegradesLoudly: the timeout backstop
// returns instead of hanging exit forever.
func TestShutdownTasksWedgedTailDegradesLoudly(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	tasks := newShutdownTasks(testLogger(), 30*time.Millisecond)

	wedge := make(chan struct{})
	t.Cleanup(func() { close(wedge) })
	tasks.watch(ctx, func() { <-wedge })

	cancel()
	start := time.Now()
	tasks.finish(false)
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("finish returned after %v, before the %v backstop", elapsed, 30*time.Millisecond)
	}
}

// TestContentArchiverLoopHoldsFirstPass: no pass before the boot-burst
// hold elapses, one promptly after.
func TestContentArchiverLoopHoldsFirstPass(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	passes := make(chan time.Time, 4)
	start := time.Now()
	go contentArchiverLoop(ctx, 60*time.Millisecond, time.Hour, func(context.Context) {
		passes <- time.Now()
	})

	select {
	case at := <-passes:
		if since := at.Sub(start); since < 60*time.Millisecond {
			t.Fatalf("first pass ran %v after start, before the 60ms hold", since)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no pass ran after the hold elapsed")
	}
}

// TestContentArchiverLoopCancelDuringHoldRunsNothing: cancellation
// inside the hold exits the loop without ever archiving.
func TestContentArchiverLoopCancelDuringHoldRunsNothing(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Bool
	done := make(chan struct{})
	go func() {
		contentArchiverLoop(ctx, time.Hour, time.Hour, func(context.Context) { ran.Store(true) })
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit on cancellation during the hold")
	}
	if ran.Load() {
		t.Fatal("a pass ran despite cancellation during the hold")
	}
}
