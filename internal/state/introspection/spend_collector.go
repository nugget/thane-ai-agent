package introspection

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/usage"
)

const (
	spendRefreshInterval = 5 * time.Minute
	spendRefreshTimeout  = 5 * time.Second
)

type spendSample struct {
	Snapshot    *usage.SpendSnapshot
	LastError   string
	LastAttempt time.Time
	Refreshing  bool
}

// SpendCollector keeps a shared recorded-spend snapshot for health tools and
// model context. Its lifecycle worker owns database reads; readers only copy
// cached state and never pay query latency. Published snapshots are immutable.
type SpendCollector struct {
	query    func(context.Context, time.Time) (*usage.SpendSnapshot, error)
	logger   *slog.Logger
	interval time.Duration
	timeout  time.Duration
	now      func() time.Time

	mu      sync.Mutex
	sample  spendSample
	running bool
}

// NewSpendCollector prepares a collector without starting work. A nil store
// produces an explicitly unavailable sample; it never represents zero spend.
// Call [SpendCollector.Run] with the application lifecycle context to refresh
// the snapshot. A nil logger uses [slog.Default].
func NewSpendCollector(store *usage.Store, logger *slog.Logger) *SpendCollector {
	if logger == nil {
		logger = slog.Default()
	}
	c := &SpendCollector{
		logger: logger, interval: spendRefreshInterval,
		timeout: spendRefreshTimeout, now: time.Now,
	}
	if store == nil {
		c.sample.LastError = "usage store is not configured"
	} else {
		c.query = store.SpendSnapshot
	}
	return c
}

// Run refreshes immediately, then every five minutes until ctx is canceled.
// Each query receives the lifecycle context with a five-second timeout. A
// failed query retains the last successful snapshot and records the error and
// attempt time; the next scheduled refresh retries. Concurrent Run calls on
// the same collector return immediately while its worker is active. Run does
// nothing for a nil collector, an unwired store, or an already-canceled ctx.
func (c *SpendCollector) Run(ctx context.Context) {
	if c == nil || c.query == nil || ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	}()

	c.refresh(ctx)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			c.refresh(ctx)
		}
	}
}

// snapshot copies metadata while sharing the immutable last-good snapshot.
// A nil Snapshot with no LastError means the first refresh is still pending.
func (c *SpendCollector) snapshot() spendSample {
	if c == nil {
		return spendSample{LastError: "spend collector is not configured"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sample
}

func (c *SpendCollector) refresh(parent context.Context) {
	asOf := c.now().UTC()
	c.mu.Lock()
	c.sample.LastAttempt = asOf
	c.sample.Refreshing = true
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	snapshot, err := c.query(ctx, asOf)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && snapshot == nil {
		err = fmt.Errorf("usage spend source returned no snapshot")
	}

	c.mu.Lock()
	c.sample.Refreshing = false
	if err != nil {
		c.sample.LastError = err.Error()
	} else {
		c.sample.Snapshot = snapshot
		c.sample.LastError = ""
	}
	c.mu.Unlock()
	if err != nil && parent.Err() == nil {
		c.logger.Warn("recorded spend refresh failed", "error", err)
	}
}
