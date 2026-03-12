package telemetry

import (
	"context"
	"database/sql"
	"log/slog"
	"math"
	"os"
	"sort"
	"time"

	"github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/usage"
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

// Collector aggregates operational metrics from multiple subsystems.
// All collection methods are safe for concurrent use — each call
// produces an independent [Metrics] snapshot.
type Collector struct {
	src Sources
}

// NewCollector creates a Collector backed by the given sources.
func NewCollector(src Sources) *Collector {
	if src.Logger == nil {
		src.Logger = slog.Default()
	}
	return &Collector{src: src}
}

// Collect gathers a point-in-time snapshot of all operational metrics.
// Individual subsystem failures are logged and result in zero values
// for the affected metrics — collection never returns an error.
func (c *Collector) Collect(ctx context.Context) *Metrics {
	m := &Metrics{
		CollectedAt: time.Now().UTC(),
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
			// File may not exist yet — normal for optional subsystems.
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

// collectLoops gathers loop registry status.
func (c *Collector) collectLoops(m *Metrics) {
	if c.src.LoopRegistry == nil {
		return
	}

	statuses := c.src.LoopRegistry.Statuses()
	m.LoopsTotal = len(statuses)
	m.LoopDetails = make([]LoopMetric, len(statuses))

	for i, s := range statuses {
		m.LoopDetails[i] = LoopMetric{
			Name:       s.Name,
			State:      string(s.State),
			Iterations: s.Iterations,
		}

		switch s.State {
		case loop.StateProcessing:
			m.LoopsActive++
		case loop.StateSleeping, loop.StateWaiting:
			m.LoopsSleeping++
		case loop.StateError:
			m.LoopsErrored++
		}
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
		tMin, err1 := time.Parse(time.RFC3339Nano, minTS)
		tMax, err2 := time.Parse(time.RFC3339Nano, maxTS)
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
