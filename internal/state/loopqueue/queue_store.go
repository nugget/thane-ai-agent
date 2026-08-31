// Package loopqueue is a durable per-consumer-loop work queue.
//
// It is the persistent, pull-based counterpart to in-memory loop
// notifications: instead of pushing an event into a live loop and waking
// it immediately (which couples trigger-rate to work-rate and lets a
// burst of events amplify into a burst of iterations), a producer
// Enqueues work for a named consumer loop, and the consumer drains its
// own partition on whatever cadence it chooses. The queue layer is
// payload-agnostic — it stores opaque JSON bytes (by convention a
// marshaled messages.LoopNotifyPayload) and never interprets them.
// Enqueue coalesces by key for frontier-style work; Append adds a
// distinct item for ordered data-plane inputs such as chat messages.
package loopqueue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// StatusPending marks an item available for draining. v1 only ever
// stores pending items (Ack deletes); the column exists so a future
// claim/visibility model can add intermediate states without a
// migration.
const StatusPending = "pending"

// AckOutcome reports whether a receipt-guarded acknowledgement removed its
// exact queue generation, found newer work, or found no pending item.
type AckOutcome string

const (
	// Acked means the exact generation was removed and journaled.
	Acked AckOutcome = "acked"
	// AckSuperseded means a newer coalesced generation remains pending.
	AckSuperseded AckOutcome = "superseded"
	// AckMissing means no item with that consumer and key remains pending.
	AckMissing AckOutcome = "missing"
)

// Item is one pending unit of work for a consumer loop. Payload is the
// opaque JSON the producer enqueued; the queue does not interpret it.
type Item struct {
	DedupKey   string
	Generation int64
	Priority   int
	EnqueuedAt time.Time
	Payload    []byte
}

// Store is a durable, deduped, per-consumer-loop work queue backed by a
// single SQLite table. It is safe for concurrent use by the *sql.DB it
// wraps.
type Store struct {
	db *sql.DB

	// WakeOnEnqueue seam (#1033): per-consumer debounced wake
	// registrations, armed at the Enqueue chokepoint. See
	// [Store.SetWakeOnEnqueue].
	wakeMu sync.Mutex
	wakes  map[string]*wakeRegistration
}

// NewStore creates a loop-queue store, running migrations on first use.
func NewStore(db *sql.DB, logger *slog.Logger) (*Store, error) {
	if err := database.Migrate(db, queueSchema, logger); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// Enqueue adds — or coalesces — one work item for consumerLoop.
//
// dedupKey is the per-consumer identity of the work (e.g.
// "session:<id>", "entity:<id>"): a second Enqueue with the same
// (consumerLoop, dedupKey) refreshes the payload and re-arms the item
// rather than appending a duplicate. The original enqueued_at is
// preserved so a repeatedly-touched subject keeps its FIFO position and
// can't starve older work; priority is raised to MAX(old, new) so a
// high-priority re-enqueue can promote but a low-priority duplicate
// can't silently demote.
func (s *Store) Enqueue(ctx context.Context, consumerLoop, dedupKey string, priority int, payload []byte) error {
	consumerLoop = strings.TrimSpace(consumerLoop)
	dedupKey = strings.TrimSpace(dedupKey)
	if consumerLoop == "" {
		return fmt.Errorf("loopqueue: consumer_loop is required")
	}
	if dedupKey == "" {
		return fmt.Errorf("loopqueue: dedup_key is required")
	}
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO loop_queue (consumer_loop, dedup_key, priority, status, attempts, payload, enqueued_at, updated_at, generation)
		VALUES (?, ?, ?, 'pending', 0, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1)
		ON CONFLICT(consumer_loop, dedup_key) DO UPDATE SET
			priority   = MAX(loop_queue.priority, excluded.priority),
			status     = 'pending',
			payload    = excluded.payload,
			updated_at = CURRENT_TIMESTAMP,
			generation = loop_queue.generation + 1
	`, consumerLoop, dedupKey, priority, string(payload))
	if err != nil {
		return fmt.Errorf("loopqueue: enqueue %s/%s: %w", consumerLoop, dedupKey, err)
	}
	// The single chokepoint the WakeOnEnqueue seam attaches to: an
	// event-driven consumer's registration turns this durable write
	// into a debounced wake (#1033). Self-paced consumers have no
	// registration and drain on their own cadence.
	s.armWake(consumerLoop)
	return nil
}

// Append adds one distinct pending item for consumerLoop and returns
// its generated queue key. Unlike [Store.Enqueue], Append never
// coalesces with existing work. keyPrefix is sanitized only enough to
// keep keys readable; callers should treat the returned key as opaque.
func (s *Store) Append(ctx context.Context, consumerLoop, keyPrefix string, priority int, payload []byte) (string, error) {
	consumerLoop = strings.TrimSpace(consumerLoop)
	if consumerLoop == "" {
		return "", fmt.Errorf("loopqueue: consumer_loop is required")
	}
	keyPrefix = strings.Trim(strings.TrimSpace(keyPrefix), ":")
	if keyPrefix == "" {
		keyPrefix = "item"
	}
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("loopqueue: generate append key: %w", err)
	}
	dedupKey := keyPrefix + ":" + id.String()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO loop_queue (consumer_loop, dedup_key, priority, status, attempts, payload, enqueued_at, updated_at, generation)
		VALUES (?, ?, ?, 'pending', 0, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1)
	`, consumerLoop, dedupKey, priority, string(payload))
	if err != nil {
		return "", fmt.Errorf("loopqueue: append %s/%s: %w", consumerLoop, dedupKey, err)
	}
	return dedupKey, nil
}

// Peek returns up to limit pending items for consumerLoop in drain order
// (priority desc, then FIFO by enqueued_at). It does NOT change item
// state: the single-consumer model acks on completion, so a crash mid-
// iteration simply leaves the item pending for the next drain
// (at-least-once). limit <= 0 is treated as 1.
func (s *Store) Peek(ctx context.Context, consumerLoop string, limit int) ([]Item, error) {
	if limit <= 0 {
		limit = 1
	}
	return s.peek(ctx, consumerLoop, limit)
}

// PeekAll returns all pending items for consumerLoop in drain order
// without changing item state.
func (s *Store) PeekAll(ctx context.Context, consumerLoop string) ([]Item, error) {
	return s.peek(ctx, consumerLoop, 0)
}

// Defer keeps one pending item while moving it behind every item currently in
// the same consumer partition. It is the non-destructive escape hatch for a
// consumer blocked on an external prerequisite: unlike [Store.Ack], no
// evidence is removed, and unlike [Store.Enqueue], the original payload is
// retained. The original enqueue timestamp also remains the queue-age and
// completion-latency anchor. A later producer enqueue may promote the item
// again.
func (s *Store) Defer(ctx context.Context, consumerLoop, dedupKey string) error {
	consumerLoop = strings.TrimSpace(consumerLoop)
	dedupKey = strings.TrimSpace(dedupKey)
	if consumerLoop == "" || dedupKey == "" {
		return fmt.Errorf("loopqueue: consumer_loop and dedup_key are required")
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE loop_queue
		SET priority = (
				SELECT COALESCE(MIN(pending.priority), 0) - 1
				FROM loop_queue AS pending
				WHERE pending.consumer_loop = ? AND pending.status = ?
			),
			attempts = attempts + 1,
			updated_at = ?
		WHERE consumer_loop = ? AND dedup_key = ? AND status = ?
	`, consumerLoop, StatusPending, now, consumerLoop, dedupKey, StatusPending)
	if err != nil {
		return fmt.Errorf("loopqueue: defer %s/%s: %w", consumerLoop, dedupKey, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("loopqueue: defer %s/%s: %w", consumerLoop, dedupKey, err)
	}
	if updated == 0 {
		return fmt.Errorf("loopqueue: cannot defer missing pending item %s/%s", consumerLoop, dedupKey)
	}
	return nil
}

func (s *Store) peek(ctx context.Context, consumerLoop string, limit int) ([]Item, error) {
	consumerLoop = strings.TrimSpace(consumerLoop)
	if consumerLoop == "" {
		return nil, fmt.Errorf("loopqueue: consumer_loop is required")
	}
	query := `
		SELECT dedup_key, generation, priority, payload, enqueued_at
		FROM loop_queue
		WHERE consumer_loop = ? AND status = ?
		ORDER BY priority DESC, enqueued_at ASC, dedup_key ASC`
	args := []any{consumerLoop, StatusPending}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Item
	for rows.Next() {
		var (
			it          Item
			payload     string
			enqueuedRaw any
		)
		if err := rows.Scan(&it.DedupKey, &it.Generation, &it.Priority, &payload, &enqueuedRaw); err != nil {
			return nil, err
		}
		it.Payload = []byte(payload)
		it.EnqueuedAt = parseQueueTimestamp(enqueuedRaw)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func parseQueueTimestamp(raw any) time.Time {
	switch v := raw.(type) {
	case time.Time:
		return v
	case string:
		if ts, err := database.ParseTimestamp(v); err == nil {
			return ts
		}
	case []byte:
		if ts, err := database.ParseTimestamp(string(v)); err == nil {
			return ts
		}
	}
	return time.Time{}
}

// escapeLikePrefix escapes the SQL LIKE wildcards ('%', '_') and the
// escape character itself so a caller-supplied string is matched as a
// literal prefix. Pair it with `ESCAPE '\'` in the query.
func escapeLikePrefix(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// PendingConsumers returns distinct consumer-loop partitions that have
// pending work. When prefix is non-empty, only partitions with that
// prefix are returned.
func (s *Store) PendingConsumers(ctx context.Context, prefix string) ([]string, error) {
	query := `
		SELECT DISTINCT consumer_loop
		FROM loop_queue
		WHERE status = ?`
	args := []any{StatusPending}
	prefix = strings.TrimSpace(prefix)
	if prefix != "" {
		// Escape LIKE wildcards so the prefix matches literally: a
		// caller passing a name fragment containing '%' or '_' must not
		// silently widen the match to unrelated partitions.
		query += ` AND consumer_loop LIKE ? ESCAPE '\'`
		args = append(args, escapeLikePrefix(prefix)+"%")
	}
	query += ` ORDER BY consumer_loop ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var consumers []string
	for rows.Next() {
		var consumer string
		if err := rows.Scan(&consumer); err != nil {
			return nil, err
		}
		consumers = append(consumers, consumer)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return consumers, nil
}

// MoveConsumer reassigns all pending work from one consumer-loop
// partition to another. It is used when callers tighten a durable
// partition key and need old rows to follow the same consumer.
func (s *Store) MoveConsumer(ctx context.Context, from, to string) error {
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if from == "" || to == "" {
		return fmt.Errorf("loopqueue: source and destination consumer_loop are required")
	}
	if from == to {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE loop_queue
		SET consumer_loop = ?, updated_at = CURRENT_TIMESTAMP
		WHERE consumer_loop = ? AND status = ?
	`, to, from, StatusPending)
	if err != nil {
		return fmt.Errorf("loopqueue: move consumer %s to %s: %w", from, to, err)
	}
	return nil
}

// Ack removes a completed item only when expectedGeneration matches the
// generation returned by [Store.Peek]. A coalescing Enqueue that lands while
// the consumer is working therefore survives the stale acknowledgement.
// Successful removal and completion journaling share one transaction. A
// missing item is idempotent and journals nothing.
func (s *Store) Ack(ctx context.Context, consumerLoop, dedupKey string, expectedGeneration int64) (AckOutcome, error) {
	consumerLoop = strings.TrimSpace(consumerLoop)
	dedupKey = strings.TrimSpace(dedupKey)
	if consumerLoop == "" || dedupKey == "" {
		return "", fmt.Errorf("loopqueue: consumer_loop and dedup_key are required")
	}
	if expectedGeneration <= 0 {
		return "", fmt.Errorf("loopqueue: expected generation must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("loopqueue: ack %s/%s: %w", consumerLoop, dedupKey, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	var (
		enqueuedRaw      any
		actualGeneration int64
	)
	err = tx.QueryRowContext(ctx,
		`SELECT enqueued_at, generation FROM loop_queue WHERE consumer_loop = ? AND dedup_key = ?`,
		consumerLoop, dedupKey,
	).Scan(&enqueuedRaw, &actualGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return AckMissing, nil
	}
	if err != nil {
		return "", fmt.Errorf("loopqueue: ack %s/%s: %w", consumerLoop, dedupKey, err)
	}
	if actualGeneration != expectedGeneration {
		return AckSuperseded, nil
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM loop_queue WHERE consumer_loop = ? AND dedup_key = ? AND generation = ?`,
		consumerLoop, dedupKey, expectedGeneration,
	)
	if err != nil {
		return "", fmt.Errorf("loopqueue: ack %s/%s: %w", consumerLoop, dedupKey, err)
	}
	// Journal only when this transaction actually removed the row. Two
	// concurrent Acks of the same key can both pass the SELECT before
	// serializing on the write; the loser's DELETE affects zero rows,
	// and journaling it anyway would double-count throughput.
	deleted, err := res.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("loopqueue: ack %s/%s: %w", consumerLoop, dedupKey, err)
	}
	if deleted == 0 {
		return AckSuperseded, nil
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO loop_queue_completions (consumer_loop, dedup_key, enqueued_at, acked_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	`, consumerLoop, dedupKey, enqueuedRaw); err != nil {
		return "", fmt.Errorf("loopqueue: journal completion %s/%s: %w", consumerLoop, dedupKey, err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("loopqueue: ack %s/%s: %w", consumerLoop, dedupKey, err)
	}
	return Acked, nil
}

// HasRecentWork reports whether a key is pending or appears in the retained
// completion journal for consumerLoop. Completion rows are intentionally
// time-bounded by [Store.PruneCompletions], so this is a recent-overlap guard,
// not permanent domain state. A durable producer cursor should remain the
// authority for whether an older source has already been traversed.
func (s *Store) HasRecentWork(ctx context.Context, consumerLoop, dedupKey string) (bool, error) {
	consumerLoop = strings.TrimSpace(consumerLoop)
	dedupKey = strings.TrimSpace(dedupKey)
	if consumerLoop == "" || dedupKey == "" {
		return false, fmt.Errorf("loopqueue: consumer_loop and dedup_key are required")
	}

	var found bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM loop_queue
			WHERE consumer_loop = ? AND dedup_key = ?
			UNION ALL
			SELECT 1 FROM loop_queue_completions
			WHERE consumer_loop = ? AND dedup_key = ?
		)
	`, consumerLoop, dedupKey, consumerLoop, dedupKey).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("loopqueue: inspect recent work %s/%s: %w", consumerLoop, dedupKey, err)
	}
	return found, nil
}

// Consumers returns the distinct consumer partitions that currently
// hold pending work, optionally filtered to names beginning with
// prefix (empty prefix = all). Used by boot-time recovery sweeps:
// debounce state is process-local, so a partition whose wake was
// pending at crash time is found here and drained directly.
func (s *Store) Consumers(ctx context.Context, prefix string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT consumer_loop FROM loop_queue
		WHERE status = ? AND consumer_loop LIKE ?
		ORDER BY consumer_loop ASC
	`, StatusPending, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var consumers []string
	for rows.Next() {
		var consumer string
		if err := rows.Scan(&consumer); err != nil {
			return nil, err
		}
		consumers = append(consumers, consumer)
	}
	return consumers, rows.Err()
}

// PendingCount returns the number of pending items in consumerLoop's
// partition. Useful for queue-health observability and tests.
func (s *Store) PendingCount(ctx context.Context, consumerLoop string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM loop_queue WHERE consumer_loop = ? AND status = ?`,
		strings.TrimSpace(consumerLoop), StatusPending,
	).Scan(&n)
	return n, err
}
