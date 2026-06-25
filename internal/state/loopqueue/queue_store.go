// Package loopqueue is a durable, deduped, per-consumer-loop work queue.
//
// It is the persistent, pull-based counterpart to in-memory loop
// notifications: instead of pushing an event into a live loop and waking
// it immediately (which couples trigger-rate to work-rate and lets a
// burst of events amplify into a burst of iterations), a producer
// Enqueues work for a named consumer loop, and the consumer drains its
// own partition on whatever cadence it chooses. The queue layer is
// payload-agnostic — it stores opaque JSON bytes (by convention a
// marshaled messages.LoopNotifyPayload) and never interprets them.
package loopqueue

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// StatusPending marks an item available for draining. v1 only ever
// stores pending items (Ack deletes); the column exists so a future
// claim/visibility model can add intermediate states without a
// migration.
const StatusPending = "pending"

// Item is one pending unit of work for a consumer loop. Payload is the
// opaque JSON the producer enqueued; the queue does not interpret it.
type Item struct {
	DedupKey   string
	Priority   int
	EnqueuedAt time.Time
	Payload    []byte
}

// Store is a durable, deduped, per-consumer-loop work queue backed by a
// single SQLite table. It is safe for concurrent use by the *sql.DB it
// wraps.
type Store struct {
	db *sql.DB
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
		INSERT INTO loop_queue (consumer_loop, dedup_key, priority, status, attempts, payload, enqueued_at, updated_at)
		VALUES (?, ?, ?, 'pending', 0, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(consumer_loop, dedup_key) DO UPDATE SET
			priority   = MAX(loop_queue.priority, excluded.priority),
			status     = 'pending',
			payload    = excluded.payload,
			updated_at = CURRENT_TIMESTAMP
	`, consumerLoop, dedupKey, priority, string(payload))
	if err != nil {
		return fmt.Errorf("loopqueue: enqueue %s/%s: %w", consumerLoop, dedupKey, err)
	}
	return nil
}

// Peek returns up to limit pending items for consumerLoop in drain order
// (priority desc, then FIFO by enqueued_at). It does NOT change item
// state: the single-consumer model acks on completion, so a crash mid-
// iteration simply leaves the item pending for the next drain
// (at-least-once). limit <= 0 is treated as 1.
func (s *Store) Peek(ctx context.Context, consumerLoop string, limit int) ([]Item, error) {
	consumerLoop = strings.TrimSpace(consumerLoop)
	if consumerLoop == "" {
		return nil, fmt.Errorf("loopqueue: consumer_loop is required")
	}
	if limit <= 0 {
		limit = 1
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT dedup_key, priority, payload, enqueued_at
		FROM loop_queue
		WHERE consumer_loop = ? AND status = ?
		ORDER BY priority DESC, enqueued_at ASC
		LIMIT ?
	`, consumerLoop, StatusPending, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Item
	for rows.Next() {
		var (
			it          Item
			payload     string
			enqueuedRaw string
		)
		if err := rows.Scan(&it.DedupKey, &it.Priority, &payload, &enqueuedRaw); err != nil {
			return nil, err
		}
		it.Payload = []byte(payload)
		if ts, perr := database.ParseTimestamp(enqueuedRaw); perr == nil {
			it.EnqueuedAt = ts
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// Ack removes a completed item from consumerLoop's partition. A missing
// (consumerLoop, dedupKey) is a no-op (idempotent).
func (s *Store) Ack(ctx context.Context, consumerLoop, dedupKey string) error {
	consumerLoop = strings.TrimSpace(consumerLoop)
	dedupKey = strings.TrimSpace(dedupKey)
	if consumerLoop == "" || dedupKey == "" {
		return fmt.Errorf("loopqueue: consumer_loop and dedup_key are required")
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM loop_queue WHERE consumer_loop = ? AND dedup_key = ?`,
		consumerLoop, dedupKey,
	)
	return err
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
