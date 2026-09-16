package introspection

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/usage"
)

func testSpendCollector(query func(context.Context, time.Time) (*usage.SpendSnapshot, error)) *SpendCollector {
	c := NewSpendCollector(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.query = query
	c.sample = spendSample{}
	c.interval = time.Hour
	c.timeout = time.Second
	return c
}

func startSpendCollector(t *testing.T, c *SpendCollector) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("spend worker did not stop on cancellation")
		}
	})
	return cancel, done
}

func awaitSpendSample(t *testing.T, c *SpendCollector, ready func(spendSample) bool) spendSample {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		sample := c.snapshot()
		if ready(sample) {
			return sample
		}
		select {
		case <-timer.C:
			t.Fatalf("spend sample did not become ready: %+v", sample)
		case <-tick.C:
		}
	}
}

func TestSpendCollectorReadsNeverQueryOrWait(t *testing.T) {
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	want := &usage.SpendSnapshot{AsOf: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	c := testSpendCollector(func(ctx context.Context, asOf time.Time) (*usage.SpendSnapshot, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return want, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	for range 100 {
		if sample := c.snapshot(); sample.Snapshot != nil || sample.LastError != "" || !sample.LastAttempt.IsZero() {
			t.Fatalf("cold read fabricated data: %+v", sample)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("cold snapshot reads queried the source")
	}
	startSpendCollector(t, c)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("initial refresh did not start")
	}
	read := make(chan spendSample, 1)
	go func() { read <- c.snapshot() }()
	select {
	case sample := <-read:
		if !sample.Refreshing || sample.Snapshot != nil || sample.LastAttempt.IsZero() {
			t.Errorf("cold refresh state=%+v", sample)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("snapshot read waited for a database query")
	}
	close(release)
	awaitSpendSample(t, c, func(s spendSample) bool { return s.Snapshot == want && !s.Refreshing })
	for range 100 {
		if c.snapshot().Snapshot != want {
			t.Fatal("warm read lost the successful snapshot")
		}
	}
	if calls.Load() != 1 {
		t.Errorf("reads triggered refreshes: calls=%d", calls.Load())
	}
}

func TestSpendCollectorRefreshRetainsLastGoodOnFailure(t *testing.T) {
	current := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	want := &usage.SpendSnapshot{AsOf: current, Last24Hours: usage.Report{Summary: usage.Summary{TotalCostUSD: 3}}}
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	c := testSpendCollector(func(ctx context.Context, _ time.Time) (*usage.SpendSnapshot, error) {
		calls++
		if calls == 1 {
			return want, nil
		}
		close(started)
		select {
		case <-release:
			return &usage.SpendSnapshot{}, errors.New("ledger unavailable")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	c.now = func() time.Time { return current }
	c.refresh(t.Context())
	current = current.Add(spendRefreshInterval)
	done := make(chan struct{})
	go func() { defer close(done); c.refresh(t.Context()) }()
	<-started
	read := make(chan spendSample, 1)
	go func() { read <- c.snapshot() }()
	select {
	case sample := <-read:
		if sample.Snapshot != want || !sample.Refreshing || sample.LastAttempt != current {
			t.Errorf("refresh lost old evidence or new attempt time: %+v", sample)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("stale read waited for refresh")
	}
	close(release)
	<-done
	sample := c.snapshot()
	if sample.Snapshot != want || sample.Refreshing || sample.LastError != "ledger unavailable" || !sample.LastAttempt.Equal(current) || sample.Snapshot.AsOf.Equal(current) {
		t.Fatalf("failed refresh replaced or restamped good data: %+v", sample)
	}
}

func TestSpendCollectorRunCoalescesAndCancels(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{}, 2)
	c := testSpendCollector(func(ctx context.Context, _ time.Time) (*usage.SpendSnapshot, error) {
		calls.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	cancel, done := startSpendCollector(t, c)
	<-started
	duplicateDone := make(chan struct{})
	go func() { defer close(duplicateDone); c.Run(t.Context()) }()
	select {
	case <-duplicateDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("duplicate Run did not return immediately")
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate worker queried source: %d calls", calls.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker ignored lifecycle cancellation")
	}
	sample := c.snapshot()
	if sample.Refreshing || sample.Snapshot != nil || sample.LastError != context.Canceled.Error() {
		t.Errorf("canceled initial read became successful data: %+v", sample)
	}
	// Finishing the first lifecycle releases ownership for a deliberate restart.
	cancelAgain, _ := startSpendCollector(t, c)
	<-started
	cancelAgain()
	if calls.Load() != 2 {
		t.Errorf("worker restart did not take ownership: %d calls", calls.Load())
	}
}

func TestSpendCollectorRefreshDeadlineAndCadence(t *testing.T) {
	var calls atomic.Int32
	c := testSpendCollector(func(ctx context.Context, _ time.Time) (*usage.SpendSnapshot, error) {
		calls.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	c.timeout = 10 * time.Millisecond
	startSpendCollector(t, c)
	sample := awaitSpendSample(t, c, func(s spendSample) bool { return !s.Refreshing && s.LastError != "" })
	if sample.Snapshot != nil || !strings.Contains(sample.LastError, context.DeadlineExceeded.Error()) {
		t.Fatalf("timeout was reported as empty success: %+v", sample)
	}
	for range 100 {
		_ = c.snapshot()
	}
	if calls.Load() != 1 {
		t.Error("failed reads retried before the next worker cadence")
	}

	t.Run("scheduled retry", func(t *testing.T) {
		var attempts atomic.Int32
		good := &usage.SpendSnapshot{}
		worker := testSpendCollector(func(context.Context, time.Time) (*usage.SpendSnapshot, error) {
			if attempts.Add(1) == 1 {
				return nil, errors.New("temporary failure")
			}
			return good, nil
		})
		worker.interval = time.Millisecond
		startSpendCollector(t, worker)
		sample := awaitSpendSample(t, worker, func(s spendSample) bool { return s.Snapshot == good })
		if sample.LastError != "" || attempts.Load() < 2 {
			t.Errorf("successful retry retained failure: %+v", sample)
		}
	})
}

func TestSpendCollectorUnavailableAndEmptyAreDistinct(t *testing.T) {
	unwired := NewSpendCollector(nil, nil)
	unwired.Run(t.Context())
	if sample := unwired.snapshot(); sample.LastError == "" || sample.Snapshot != nil || sample.Refreshing {
		t.Fatalf("unwired collector invented zero spend: %+v", sample)
	}
	for _, tc := range []struct {
		name      string
		response  *usage.SpendSnapshot
		wantError bool
	}{
		{"invalid nil response", nil, true},
		{"genuinely empty ledger", &usage.SpendSnapshot{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testSpendCollector(func(context.Context, time.Time) (*usage.SpendSnapshot, error) { return tc.response, nil })
			c.refresh(t.Context())
			if sample := c.snapshot(); (sample.LastError != "") != tc.wantError || sample.Snapshot != tc.response {
				t.Errorf("availability state=%+v", sample)
			}
		})
	}
}
