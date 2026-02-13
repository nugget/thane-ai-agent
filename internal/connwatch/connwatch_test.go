package connwatch

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// testBackoff returns a fast backoff config for tests.
func testBackoff() BackoffConfig {
	return BackoffConfig{
		InitialDelay: 1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Multiplier:   2.0,
		MaxRetries:   5,
		PollInterval: 5 * time.Millisecond,
		ProbeTimeout: 100 * time.Millisecond,
	}
}

// waitFor polls cond every tick until it returns true or timeout elapses.
// Returns true if the condition was met, false on timeout.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(1 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for condition: %s", msg)
}

func TestDefaultBackoffConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultBackoffConfig()

	if cfg.InitialDelay != 2*time.Second {
		t.Errorf("InitialDelay = %v, want 2s", cfg.InitialDelay)
	}
	if cfg.MaxDelay != 60*time.Second {
		t.Errorf("MaxDelay = %v, want 60s", cfg.MaxDelay)
	}
	if cfg.Multiplier != 2.0 {
		t.Errorf("Multiplier = %v, want 2.0", cfg.Multiplier)
	}
	if cfg.MaxRetries != 10 {
		t.Errorf("MaxRetries = %d, want 10", cfg.MaxRetries)
	}
	if cfg.PollInterval != 60*time.Second {
		t.Errorf("PollInterval = %v, want 60s", cfg.PollInterval)
	}
	if cfg.ProbeTimeout != 10*time.Second {
		t.Errorf("ProbeTimeout = %v, want 10s", cfg.ProbeTimeout)
	}
}

func TestWatcher_ImmediateSuccess(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var readyCalled atomic.Int32

	m := NewManager(slog.Default())
	w := m.Watch(ctx, WatcherConfig{
		Name:    "test-immediate",
		Probe:   func(ctx context.Context) error { return nil },
		Backoff: testBackoff(),
		OnReady: func() { readyCalled.Add(1) },
	})

	waitFor(t, 2*time.Second, w.IsReady, "IsReady() == true")

	if w.LastError() != nil {
		t.Errorf("expected nil LastError, got %v", w.LastError())
	}
	if readyCalled.Load() != 1 {
		t.Errorf("OnReady called %d times, want 1", readyCalled.Load())
	}
}

func TestWatcher_BackoffThenSuccess(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errDown := errors.New("service down")
	var attempts atomic.Int32

	// Fail 3 times, then succeed.
	probe := func(ctx context.Context) error {
		n := attempts.Add(1)
		if n <= 3 {
			return errDown
		}
		return nil
	}

	var readyCalled atomic.Int32

	m := NewManager(slog.Default())
	w := m.Watch(ctx, WatcherConfig{
		Name:    "test-backoff",
		Probe:   probe,
		Backoff: testBackoff(),
		OnReady: func() { readyCalled.Add(1) },
	})

	waitFor(t, 2*time.Second, w.IsReady, "IsReady() == true after retries")

	if readyCalled.Load() != 1 {
		t.Errorf("OnReady called %d times, want 1", readyCalled.Load())
	}
	if n := attempts.Load(); n < 4 {
		t.Errorf("expected at least 4 probe attempts, got %d", n)
	}
}

func TestWatcher_ExhaustsRetries(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errDown := errors.New("always down")
	var attempts atomic.Int32

	m := NewManager(slog.Default())
	w := m.Watch(ctx, WatcherConfig{
		Name:    "test-exhaust",
		Probe:   func(ctx context.Context) error { attempts.Add(1); return errDown },
		Backoff: testBackoff(),
	})

	// Wait until all startup retries have been attempted.
	waitFor(t, 2*time.Second, func() bool {
		return attempts.Load() >= 5
	}, "all startup retries attempted")

	if w.IsReady() {
		t.Error("expected IsReady() == false after exhausting retries")
	}
	if w.LastError() == nil {
		t.Error("expected non-nil LastError")
	}
}

func TestWatcher_ServiceGoesDown(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errDown := errors.New("went down")
	var shouldFail atomic.Bool

	probe := func(ctx context.Context) error {
		if shouldFail.Load() {
			return errDown
		}
		return nil
	}

	var downCalled atomic.Int32

	m := NewManager(slog.Default())
	w := m.Watch(ctx, WatcherConfig{
		Name:    "test-goes-down",
		Probe:   probe,
		Backoff: testBackoff(),
		OnDown:  func(err error) { downCalled.Add(1) },
	})

	waitFor(t, 2*time.Second, w.IsReady, "initially ready")

	// Make the service fail.
	shouldFail.Store(true)

	// Wait for at least one poll cycle to detect the failure.
	waitFor(t, 2*time.Second, func() bool {
		return !w.IsReady()
	}, "IsReady() == false after failure")

	if downCalled.Load() < 1 {
		t.Errorf("OnDown called %d times, want >= 1", downCalled.Load())
	}
}

func TestWatcher_ServiceRecovers(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errDown := errors.New("down")
	var shouldFail atomic.Bool
	shouldFail.Store(true) // start failing

	probe := func(ctx context.Context) error {
		if shouldFail.Load() {
			return errDown
		}
		return nil
	}

	var readyCalled atomic.Int32

	bcfg := testBackoff()
	bcfg.MaxRetries = 2 // exhaust quickly

	m := NewManager(slog.Default())
	w := m.Watch(ctx, WatcherConfig{
		Name:    "test-recovers",
		Probe:   probe,
		Backoff: bcfg,
		OnReady: func() { readyCalled.Add(1) },
	})

	// Wait for startup retries to exhaust.
	waitFor(t, 2*time.Second, func() bool {
		return w.LastError() != nil
	}, "startup retries exhausted")

	if w.IsReady() {
		t.Fatal("expected not ready after startup exhaustion")
	}

	// Recover the service.
	shouldFail.Store(false)

	// Wait for background poll to detect recovery.
	waitFor(t, 2*time.Second, w.IsReady, "IsReady() == true after recovery")

	if readyCalled.Load() < 1 {
		t.Errorf("OnReady called %d times, want >= 1", readyCalled.Load())
	}
}

func TestWatcher_ContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	errDown := errors.New("down")
	m := NewManager(slog.Default())
	w := m.Watch(ctx, WatcherConfig{
		Name:    "test-cancel",
		Probe:   func(ctx context.Context) error { return errDown },
		Backoff: testBackoff(),
	})

	// Cancel context and verify the watcher stops.
	cancel()

	done := make(chan struct{})
	go func() {
		w.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Good, watcher stopped.
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop after context cancellation")
	}
}

func TestWatcher_Stop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	m := NewManager(slog.Default())
	w := m.Watch(ctx, WatcherConfig{
		Name:    "test-stop",
		Probe:   func(ctx context.Context) error { return nil },
		Backoff: testBackoff(),
	})

	waitFor(t, 2*time.Second, w.IsReady, "ready before stop")

	// Stop should return promptly.
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within timeout")
	}
}

func TestWatcher_ProbeTimeout(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Probe that blocks until context expires.
	probe := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}

	bcfg := testBackoff()
	bcfg.ProbeTimeout = 5 * time.Millisecond
	bcfg.MaxRetries = 1

	m := NewManager(slog.Default())
	w := m.Watch(ctx, WatcherConfig{
		Name:    "test-probe-timeout",
		Probe:   probe,
		Backoff: bcfg,
	})

	waitFor(t, 2*time.Second, func() bool {
		return w.LastError() != nil
	}, "probe error recorded after timeout")

	if w.IsReady() {
		t.Error("expected not ready when probe always times out")
	}
}

func TestWatcher_OnReadyNotCalledWhenAlreadyReady(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var readyCalled atomic.Int32
	var probeCount atomic.Int32

	m := NewManager(slog.Default())
	_ = m.Watch(ctx, WatcherConfig{
		Name: "test-already-ready",
		Probe: func(ctx context.Context) error {
			probeCount.Add(1)
			return nil
		},
		Backoff: testBackoff(),
		OnReady: func() { readyCalled.Add(1) },
	})

	// Wait for multiple poll cycles to pass.
	waitFor(t, 2*time.Second, func() bool {
		return probeCount.Load() >= 3
	}, "at least 3 probes completed")

	// OnReady should be called exactly once (startup), not on every successful poll.
	if n := readyCalled.Load(); n != 1 {
		t.Errorf("OnReady called %d times, want exactly 1", n)
	}
}

func TestManager_MultipleWatchers(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errDown := errors.New("down")

	m := NewManager(slog.Default())

	w1 := m.Watch(ctx, WatcherConfig{
		Name:    "svc-a",
		Probe:   func(ctx context.Context) error { return nil },
		Backoff: testBackoff(),
	})

	var attempts atomic.Int32
	bcfg := testBackoff()
	bcfg.MaxRetries = 1 // exhaust quickly
	w2 := m.Watch(ctx, WatcherConfig{
		Name:    "svc-b",
		Probe:   func(ctx context.Context) error { attempts.Add(1); return errDown },
		Backoff: bcfg,
	})

	waitFor(t, 2*time.Second, w1.IsReady, "svc-a ready")
	waitFor(t, 2*time.Second, func() bool {
		return attempts.Load() >= 1
	}, "svc-b attempted")

	if w2.IsReady() {
		t.Error("svc-b should not be ready")
	}
}

func TestManager_Status(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManager(slog.Default())

	w1 := m.Watch(ctx, WatcherConfig{
		Name:    "healthy-svc",
		Probe:   func(ctx context.Context) error { return nil },
		Backoff: testBackoff(),
	})

	var downAttempts atomic.Int32
	bcfg := testBackoff()
	bcfg.MaxRetries = 1
	m.Watch(ctx, WatcherConfig{
		Name:    "down-svc",
		Probe:   func(ctx context.Context) error { downAttempts.Add(1); return errors.New("unreachable") },
		Backoff: bcfg,
	})

	waitFor(t, 2*time.Second, w1.IsReady, "healthy-svc ready")
	waitFor(t, 2*time.Second, func() bool {
		return downAttempts.Load() >= 1
	}, "down-svc attempted")

	status := m.Status()

	if len(status) != 2 {
		t.Fatalf("expected 2 entries in Status, got %d", len(status))
	}

	if s, ok := status["healthy-svc"]; !ok {
		t.Error("missing healthy-svc in Status")
	} else {
		if !s.Ready {
			t.Error("healthy-svc should be ready")
		}
		if s.LastError != "" {
			t.Errorf("healthy-svc should have no error, got %q", s.LastError)
		}
	}

	if s, ok := status["down-svc"]; !ok {
		t.Error("missing down-svc in Status")
	} else {
		if s.Ready {
			t.Error("down-svc should not be ready")
		}
		if s.LastError == "" {
			t.Error("down-svc should have an error")
		}
	}
}

func TestManager_Stop(t *testing.T) {
	t.Parallel()

	m := NewManager(slog.Default())

	w := m.Watch(context.Background(), WatcherConfig{
		Name:    "svc-1",
		Probe:   func(ctx context.Context) error { return nil },
		Backoff: testBackoff(),
	})
	m.Watch(context.Background(), WatcherConfig{
		Name:    "svc-2",
		Probe:   func(ctx context.Context) error { return nil },
		Backoff: testBackoff(),
	})

	waitFor(t, 2*time.Second, w.IsReady, "svc-1 ready before stop")

	done := make(chan struct{})
	go func() {
		m.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Manager.Stop did not return within timeout")
	}
}

func TestWatch_PanicsOnEmptyName(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for empty Name")
		}
	}()

	m := NewManager(slog.Default())
	m.Watch(context.Background(), WatcherConfig{
		Name:    "",
		Probe:   func(ctx context.Context) error { return nil },
		Backoff: testBackoff(),
	})
}

func TestWatch_PanicsOnNilProbe(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil Probe")
		}
	}()

	m := NewManager(slog.Default())
	m.Watch(context.Background(), WatcherConfig{
		Name:    "test-nil-probe",
		Probe:   nil,
		Backoff: testBackoff(),
	})
}

func TestWatch_DefaultsZeroBackoffFields(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManager(slog.Default())
	// Pass a completely zero BackoffConfig — all fields should get defaults.
	w := m.Watch(ctx, WatcherConfig{
		Name:  "test-defaults",
		Probe: func(ctx context.Context) error { return nil },
	})

	waitFor(t, 2*time.Second, w.IsReady, "ready with defaulted backoff")
}
