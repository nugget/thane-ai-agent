package loopqueue

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DefaultCompletionRetention is how long journaled completions are kept
// before the daily prune removes them. Thirty days covers a full
// month-scale incident retrospective; anything older belongs to the log
// pipeline, not a hot audit table on thane.db.
const DefaultCompletionRetention = 30 * 24 * time.Hour

// ConsumerPending summarizes one partition's live backlog: how many
// items are pending and how long the oldest has been waiting.
type ConsumerPending struct {
	Consumer         string
	Pending          int
	OldestEnqueuedAt time.Time
}

// PendingStats returns the live backlog per consumer partition, sorted
// by consumer name for deterministic output. Partitions with no pending
// work do not appear.
func (s *Store) PendingStats(ctx context.Context) ([]ConsumerPending, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT consumer_loop, COUNT(*), MIN(enqueued_at)
		FROM loop_queue
		WHERE status = ?
		GROUP BY consumer_loop
		ORDER BY consumer_loop ASC
	`, StatusPending)
	if err != nil {
		return nil, fmt.Errorf("loopqueue: pending stats: %w", err)
	}
	defer rows.Close()

	var stats []ConsumerPending
	for rows.Next() {
		var (
			cp        ConsumerPending
			oldestRaw any
		)
		if err := rows.Scan(&cp.Consumer, &cp.Pending, &oldestRaw); err != nil {
			return nil, err
		}
		cp.OldestEnqueuedAt = parseQueueTimestamp(oldestRaw)
		stats = append(stats, cp)
	}
	return stats, rows.Err()
}

// PendingFor returns one consumer's live backlog: how many items are
// pending and when the oldest was enqueued. A partition with no pending
// work reports zero and a zero time.
//
// Separate from [Store.PendingStats] because the hot caller is a single
// loop asking about itself on every pull, and scanning every partition
// to answer that would make the common case pay for the census.
func (s *Store) PendingFor(ctx context.Context, consumer string) (pending int, oldest time.Time, err error) {
	var oldestRaw any
	row := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), MIN(enqueued_at)
		FROM loop_queue
		WHERE status = ? AND consumer_loop = ?
	`, StatusPending, strings.TrimSpace(consumer))
	if err := row.Scan(&pending, &oldestRaw); err != nil {
		return 0, time.Time{}, fmt.Errorf("loopqueue: pending for %q: %w", consumer, err)
	}
	return pending, parseQueueTimestamp(oldestRaw), nil
}

// PendingCounts returns pending depth keyed by consumer partition —
// the join shape loop views use to attach queue depth to a loop row.
// Partitions with no pending work are absent from the map.
func (s *Store) PendingCounts(ctx context.Context) (map[string]int, error) {
	stats, err := s.PendingStats(ctx)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(stats))
	for _, cp := range stats {
		counts[cp.Consumer] = cp.Pending
	}
	return counts, nil
}

// Completion is one journaled acknowledgment: what was completed, for
// whom, and how long it waited between enqueue and ack.
type Completion struct {
	Consumer   string
	DedupKey   string
	EnqueuedAt time.Time
	AckedAt    time.Time
}

// Waited is the enqueue→ack span, floored at zero so clock skew in
// stored timestamps can never report a negative wait.
func (c Completion) Waited() time.Duration {
	d := c.AckedAt.Sub(c.EnqueuedAt)
	if d < 0 {
		return 0
	}
	return d
}

// RecentCompletions returns journaled acks newest-first, optionally
// scoped to one consumer partition, bounded by since and limit
// (limit <= 0 defaults to 20).
func (s *Store) RecentCompletions(ctx context.Context, consumer string, since time.Time, limit int) ([]Completion, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `
		SELECT consumer_loop, dedup_key, enqueued_at, acked_at
		FROM loop_queue_completions
		WHERE acked_at >= ?`
	args := []any{since.UTC()}
	consumer = strings.TrimSpace(consumer)
	if consumer != "" {
		query += ` AND consumer_loop = ?`
		args = append(args, consumer)
	}
	query += ` ORDER BY acked_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("loopqueue: recent completions: %w", err)
	}
	defer rows.Close()

	var completions []Completion
	for rows.Next() {
		var (
			c           Completion
			enqueuedRaw any
			ackedRaw    any
		)
		if err := rows.Scan(&c.Consumer, &c.DedupKey, &enqueuedRaw, &ackedRaw); err != nil {
			return nil, err
		}
		c.EnqueuedAt = parseQueueTimestamp(enqueuedRaw)
		c.AckedAt = parseQueueTimestamp(ackedRaw)
		completions = append(completions, c)
	}
	return completions, rows.Err()
}

// CompletionStats aggregates one consumer's journaled acks over a
// window: count and the average/max enqueue→ack wait.
type CompletionStats struct {
	Consumer string
	Count    int
	AvgWait  time.Duration
	MaxWait  time.Duration
}

// CompletionStatsSince aggregates journaled acks per consumer since the
// given instant, sorted by consumer name. The wait math runs in SQL
// (julianday spans) so callers never re-derive it; negative spans from
// skewed stored timestamps clamp to zero.
func (s *Store) CompletionStatsSince(ctx context.Context, since time.Time) ([]CompletionStats, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT consumer_loop,
			COUNT(*),
			AVG(MAX((julianday(acked_at) - julianday(enqueued_at)) * 86400.0, 0)),
			MAX(MAX((julianday(acked_at) - julianday(enqueued_at)) * 86400.0, 0))
		FROM loop_queue_completions
		WHERE acked_at >= ?
		GROUP BY consumer_loop
		ORDER BY consumer_loop ASC
	`, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("loopqueue: completion stats: %w", err)
	}
	defer rows.Close()

	var stats []CompletionStats
	for rows.Next() {
		var (
			cs      CompletionStats
			avgSecs float64
			maxSecs float64
		)
		if err := rows.Scan(&cs.Consumer, &cs.Count, &avgSecs, &maxSecs); err != nil {
			return nil, err
		}
		cs.AvgWait = time.Duration(avgSecs * float64(time.Second))
		cs.MaxWait = time.Duration(maxSecs * float64(time.Second))
		stats = append(stats, cs)
	}
	return stats, rows.Err()
}

// PruneCompletions deletes journaled completions older than maxAge and
// reports how many rows were removed. Called from the app's daily
// maintenance worker; the journal is an audit trail, not history —
// long-term forensics belong to the log pipeline.
func (s *Store) PruneCompletions(ctx context.Context, maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		return 0, fmt.Errorf("loopqueue: prune requires a positive max age")
	}
	cutoff := time.Now().UTC().Add(-maxAge)
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM loop_queue_completions WHERE acked_at < ?`, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("loopqueue: prune completions: %w", err)
	}
	return res.RowsAffected()
}
