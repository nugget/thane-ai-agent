package telemetry

import (
	"context"
	"database/sql"
	"log/slog"
	"math"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// ArchiveSource provides active session counts for telemetry without
// coupling this package to the full memory package.
type ArchiveSource interface {
	ActiveSessionCount() (int, error)
}

// AttachmentSource provides aggregate attachment statistics without
// coupling this package to the full attachments package.
type AttachmentSource interface {
	TelemetryStats(ctx context.Context) (total, totalBytes, unique int64, err error)
}

// Sources holds references to all subsystems that provide telemetry
// data. Nil sources are handled gracefully — the corresponding
// metrics are reported as zero.
type Sources struct {
	LoopRegistry     *loop.Registry
	UsageStore       *usage.Store
	ArchiveStore     ArchiveSource
	LogsDB           *sql.DB
	AttachmentSource AttachmentSource
	DBPaths          map[string]string // name → file path for os.Stat
	Logger           *slog.Logger
}

// collectorCacheTTL bounds how stale a served snapshot may be before a
// background refresh is triggered. The heavy collectors scan a day of
// log rows and the whole attachments table; on a production-sized
// logs.db that is ~2s of query time, which once ran inline in context
// assembly on every ops-panel render and starved everything scheduled
// after it on the shared budget.
const collectorCacheTTL = 5 * time.Minute

// collectorRefreshTimeout is the detached budget for one background
// refresh, deliberately independent of any caller's deadline.
const collectorRefreshTimeout = 60 * time.Second

// Collector aggregates operational metrics from multiple subsystems.
// All methods are safe for concurrent use. Snapshots returned by
// Collect/CollectFresh are shared and must be treated as read-only.
type Collector struct {
	src Sources
	ttl time.Duration
	now func() time.Time // injectable clock; nil uses time.Now

	mu         sync.Mutex
	cached     *Metrics
	refreshing bool
}

// NewCollector creates a Collector backed by the given sources.
func NewCollector(src Sources) *Collector {
	if src.Logger == nil {
		src.Logger = slog.Default()
	}
	return &Collector{src: src, ttl: collectorCacheTTL}
}

func (c *Collector) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Collect returns the current snapshot without ever paying the
// collection cost inline once a snapshot exists: a fresh cache is
// returned as-is, a stale one is returned immediately while a single
// background refresh (on its own detached budget) rebuilds it. Only a
// cold start — no snapshot yet — collects synchronously under the
// caller's ctx. This is the assembly-path contract: the ops panel
// renders whatever is current, at most collectorCacheTTL old, and the
// shared context budget never funds a day-of-logs scan. CollectedAt
// tells readers how old the snapshot is.
func (c *Collector) Collect(ctx context.Context) *Metrics {
	c.mu.Lock()
	cached := c.cached
	if cached != nil {
		if c.clock().UTC().Sub(cached.CollectedAt) >= c.ttl && !c.refreshing {
			c.refreshing = true
			go c.refresh()
		}
		c.mu.Unlock()
		return cached
	}
	c.mu.Unlock()

	m := c.collect(ctx)
	// A snapshot truncated by the caller's deadline must not become the
	// cache: it would serve zeros as facts for a full TTL.
	if ctx.Err() == nil {
		c.mu.Lock()
		if c.cached == nil {
			c.cached = m
		}
		c.mu.Unlock()
	}
	return m
}

// CollectFresh always performs a full collection under the caller's ctx
// and replaces the cache. The MQTT publisher uses this: its cadence is
// long, so serving it the cache would compound both delays into sensor
// readings a TTL-plus-interval old.
func (c *Collector) CollectFresh(ctx context.Context) *Metrics {
	m := c.collect(ctx)
	if ctx.Err() == nil {
		c.mu.Lock()
		c.cached = m
		c.mu.Unlock()
	}
	return m
}

func (c *Collector) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), collectorRefreshTimeout)
	defer cancel()
	m := c.collect(ctx)
	c.mu.Lock()
	// A refresh its own budget truncated keeps the old snapshot: stale
	// real numbers beat fresh partial zeros, the next Collect retries,
	// and the per-collector warns already name what timed out.
	if ctx.Err() == nil {
		c.cached = m
	}
	c.refreshing = false
	c.mu.Unlock()
}

// collect gathers a point-in-time snapshot of all operational metrics.
// Individual subsystem failures are logged and result in zero values
// for the affected metrics — collection never returns an error.
func (c *Collector) collect(ctx context.Context) *Metrics {
	m := &Metrics{
		CollectedAt: c.clock().UTC(),
		DBSizes:     make(map[string]int64),
	}

	c.collectDBSizes(m)
	c.collectTokens(ctx, m)
	c.collectSessions(m)
	c.collectLoops(m)
	c.collectRequests(ctx, m)
	c.collectAttachments(ctx, m)

	return m
}

// collectDBSizes stat's each configured database file.
func (c *Collector) collectDBSizes(m *Metrics) {
	for name, path := range c.src.DBPaths {
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			if !os.IsNotExist(err) {
				c.src.Logger.Warn("telemetry: stat db file failed",
					"db", name, "path", path, "error", err)
			}
			continue
		}
		m.DBSizes[name] = info.Size()
	}
}

// collectTokens queries 24h rolling token usage.
func (c *Collector) collectTokens(ctx context.Context, m *Metrics) {
	if c.src.UsageStore == nil {
		return
	}
	_ = ctx // usage.Store methods don't take ctx yet

	now := time.Now().UTC()
	start := now.Add(-24 * time.Hour)

	summary, err := c.src.UsageStore.Summary(start, now)
	if err != nil {
		c.src.Logger.Warn("telemetry: token summary failed", "error", err)
		return
	}
	m.TokensInput = summary.TotalInputTokens
	m.TokensOutput = summary.TotalOutputTokens
	m.TokensCost = summary.TotalCostUSD

	byModel, err := c.src.UsageStore.SummaryByModel(start, now)
	if err != nil {
		c.src.Logger.Warn("telemetry: token by-model failed", "error", err)
		return
	}
	if len(byModel) > 0 {
		m.TokensByModel = make(map[string]ModelTokens, len(byModel))
		for _, gs := range byModel {
			m.TokensByModel[gs.Key] = ModelTokens{
				Input:  gs.Summary.TotalInputTokens,
				Output: gs.Summary.TotalOutputTokens,
				Cost:   gs.Summary.TotalCostUSD,
			}
		}
	}
}

// collectSessions counts active sessions and estimates context utilization.
func (c *Collector) collectSessions(m *Metrics) {
	if c.src.ArchiveStore != nil {
		count, err := c.src.ArchiveStore.ActiveSessionCount()
		if err != nil {
			c.src.Logger.Warn("telemetry: active session count failed", "error", err)
		} else {
			m.ActiveSessions = count
		}
	}

	// Context utilization: find the main interactive loop and compute
	// token usage as a percentage of context window.
	if c.src.LoopRegistry != nil {
		for _, status := range c.src.LoopRegistry.Statuses() {
			if status.Name == "interactive" && status.ContextWindow > 0 {
				m.ContextUtilization = float64(status.TotalInputTokens+status.TotalOutputTokens) /
					float64(status.ContextWindow) * 100
				if m.ContextUtilization > 100 {
					m.ContextUtilization = 100
				}
				break
			}
		}
	}
}

// collectLoops gathers loop registry status. Child loops (those with a
// ParentID) are counted in aggregate totals but excluded from
// LoopDetails — they are ephemeral, their auto-generated names may
// contain private conversation content, and their single-iteration
// metrics aren't useful as persistent HA sensors. The dashboard shows
// child loops as nested nodes under their parent; that is their
// visibility surface.
func (c *Collector) collectLoops(m *Metrics) {
	if c.src.LoopRegistry == nil {
		return
	}

	statuses := c.src.LoopRegistry.Statuses()
	m.LoopsTotal = len(statuses)

	for _, s := range statuses {
		switch s.State {
		case loop.StateProcessing:
			m.LoopsActive++
		case loop.StateSleeping, loop.StateWaiting:
			m.LoopsSleeping++
		case loop.StateError:
			m.LoopsErrored++
		}

		// Skip child loops from MQTT telemetry publishing.
		if s.ParentID != "" {
			continue
		}

		m.LoopDetails = append(m.LoopDetails, LoopMetric{
			Name:       s.Name,
			State:      string(s.State),
			Iterations: s.Iterations,
		})
	}
}

// collectRequests queries the log index for 24h request and error counts,
// and computes approximate p50/p95 latencies from request durations.
func (c *Collector) collectRequests(ctx context.Context, m *Metrics) {
	if c.src.LogsDB == nil {
		return
	}

	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour).Format(time.RFC3339)

	// Count distinct request IDs (non-empty) in the last 24h.
	var reqCount int
	err := c.src.LogsDB.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT request_id) FROM log_entries
		 WHERE request_id != '' AND timestamp >= ?`, since,
	).Scan(&reqCount)
	if err != nil {
		c.src.Logger.Warn("telemetry: request count query failed", "error", err)
	}
	m.Requests24h = reqCount

	// Count error-level entries in the last 24h.
	var errCount int
	err = c.src.LogsDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM log_entries
		 WHERE level = 'ERROR' AND timestamp >= ?`, since,
	).Scan(&errCount)
	if err != nil {
		c.src.Logger.Warn("telemetry: error count query failed", "error", err)
	}
	m.Errors24h = errCount

	// Compute request latencies: for each request_id, duration = max(ts) - min(ts).
	rows, err := c.src.LogsDB.QueryContext(ctx,
		`SELECT request_id, MIN(timestamp), MAX(timestamp)
		 FROM log_entries
		 WHERE request_id != '' AND timestamp >= ?
		 GROUP BY request_id
		 HAVING COUNT(*) > 1`, since,
	)
	if err != nil {
		c.src.Logger.Warn("telemetry: latency query failed", "error", err)
		return
	}
	defer rows.Close()

	var durations []float64
	for rows.Next() {
		var reqID, minTS, maxTS string
		if err := rows.Scan(&reqID, &minTS, &maxTS); err != nil {
			continue
		}
		tMin, err1 := database.ParseTimestamp(minTS)
		tMax, err2 := database.ParseTimestamp(maxTS)
		if err1 != nil || err2 != nil {
			continue
		}
		ms := tMax.Sub(tMin).Seconds() * 1000
		if ms > 0 {
			durations = append(durations, ms)
		}
	}

	if len(durations) > 0 {
		sort.Float64s(durations)
		m.LatencyP50Ms = percentile(durations, 50)
		m.LatencyP95Ms = percentile(durations, 95)
	}
}

// percentile computes the p-th percentile of sorted data using linear
// interpolation.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := p / 100 * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := lower + 1
	if upper >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := rank - float64(lower)
	return sorted[lower] + frac*(sorted[upper]-sorted[lower])
}

// collectAttachments queries aggregate attachment store statistics.
func (c *Collector) collectAttachments(ctx context.Context, m *Metrics) {
	if c.src.AttachmentSource == nil {
		return
	}

	total, totalBytes, unique, err := c.src.AttachmentSource.TelemetryStats(ctx)
	if err != nil {
		c.src.Logger.Warn("telemetry: attachment stats failed", "error", err)
		return
	}
	m.AttachmentsTotal = total
	m.AttachmentsTotalBytes = totalBytes
	m.AttachmentsUnique = unique
}
