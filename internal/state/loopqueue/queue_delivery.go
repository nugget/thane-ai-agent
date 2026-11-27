package loopqueue

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// StatusSetAside marks an item its consumer stopped delivering because
// every counted delivery failed to complete it. The row keeps its
// payload, enqueue time and attempts. Every pending reader filters on
// [StatusPending], so a set-aside row is out of drain, depth, wake and
// rehydration; Ack ignores it. Only Append-fed partitions (loop
// mailboxes) are set aside: Enqueue's coalescing upsert would return a
// set-aside row with the same key to pending.
const StatusSetAside = "set_aside"

// RecordDelivery adds one to attempts on each pending item about to be
// handed to a turn. It commits before the turn starts, so a process that
// dies during the turn leaves the count behind. One transaction; rows
// whose receipt or status changed since the peek are skipped.
func (s *Store) RecordDelivery(ctx context.Context, consumerLoop string, items []Item) error {
	return s.updatePending(ctx, "record delivery", consumerLoop, items,
		`UPDATE loop_queue SET attempts = attempts + 1, updated_at = ?
		WHERE consumer_loop = ? AND dedup_key = ? AND receipt = ? AND status = ?`)
}

// RefundDelivery takes back one counted delivery from each pending item,
// never below zero, for a failure the items did not cause.
func (s *Store) RefundDelivery(ctx context.Context, consumerLoop string, items []Item) error {
	return s.updatePending(ctx, "refund delivery", consumerLoop, items,
		`UPDATE loop_queue SET attempts = MAX(attempts - 1, 0), updated_at = ?
		WHERE consumer_loop = ? AND dedup_key = ? AND receipt = ? AND status = ?`)
}

// PutAside moves pending items to [StatusSetAside] and reports how many
// rows changed. updated_at records when the item was set aside.
func (s *Store) PutAside(ctx context.Context, consumerLoop string, items []Item) (int, error) {
	var changed int
	err := s.withPendingRows(ctx, "put aside", consumerLoop, items, func(tx *sql.Tx, now time.Time, it Item) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE loop_queue SET status = ?, updated_at = ?
			WHERE consumer_loop = ? AND dedup_key = ? AND receipt = ? AND status = ?`,
			StatusSetAside, now, consumerLoop, it.DedupKey, it.Receipt, StatusPending)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		changed += int(n)
		return err
	})
	if err != nil {
		return 0, err
	}
	return changed, nil
}

func (s *Store) updatePending(ctx context.Context, op, consumerLoop string, items []Item, query string) error {
	return s.withPendingRows(ctx, op, consumerLoop, items, func(tx *sql.Tx, now time.Time, it Item) error {
		_, err := tx.ExecContext(ctx, query, now, consumerLoop, it.DedupKey, it.Receipt, StatusPending)
		return err
	})
}

// withPendingRows runs one write per item in a single transaction. The
// transaction issues no read before its first write, so SQLite takes
// the write lock under busy_timeout instead of failing a read-to-write
// upgrade against a concurrent writer.
func (s *Store) withPendingRows(ctx context.Context, op, consumerLoop string, items []Item, write func(*sql.Tx, time.Time, Item) error) error {
	consumerLoop = strings.TrimSpace(consumerLoop)
	if consumerLoop == "" {
		return fmt.Errorf("loopqueue: %s: consumer_loop is required", op)
	}
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("loopqueue: %s %s: %w", op, consumerLoop, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	now := time.Now().UTC()
	for _, it := range items {
		if err := write(tx, now, it); err != nil {
			return fmt.Errorf("loopqueue: %s %s/%s: %w", op, consumerLoop, it.DedupKey, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("loopqueue: %s %s: %w", op, consumerLoop, err)
	}
	return nil
}

// ConsumerSetAside summarizes one partition's set-aside items since a
// cutoff: how many, and when the newest was set aside.
type ConsumerSetAside struct {
	Consumer         string
	Count            int
	NewestSetAsideAt time.Time
}

// CountSetAside returns, per partition and sorted by name, the items set
// aside at or after since. Partitions with none do not appear.
func (s *Store) CountSetAside(ctx context.Context, since time.Time) ([]ConsumerSetAside, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT consumer_loop, COUNT(*), MAX(updated_at)
		FROM loop_queue
		WHERE status = ? AND updated_at >= ?
		GROUP BY consumer_loop
		ORDER BY consumer_loop ASC
	`, StatusSetAside, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("loopqueue: count set-aside: %w", err)
	}
	defer rows.Close()
	var out []ConsumerSetAside
	for rows.Next() {
		var (
			cs        ConsumerSetAside
			newestRaw any
		)
		if err := rows.Scan(&cs.Consumer, &cs.Count, &newestRaw); err != nil {
			return nil, err
		}
		cs.NewestSetAsideAt = parseQueueTimestamp(newestRaw)
		out = append(out, cs)
	}
	return out, rows.Err()
}
