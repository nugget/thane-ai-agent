package loopqueue

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// PendingSplit divides one partition's pending work at a mark: a point
// in its enqueue history that an earlier [Store.SplitPending] returned.
type PendingSplit struct {
	// Through counts the pending items last enqueued at or before the
	// mark. A Defer does not move an item past the mark; only a new
	// Enqueue of the same key does.
	Through int
	// Since counts the pending items enqueued, or enqueued again, after
	// the mark was taken.
	Since int
	// Mark is the partition's mark now, to pass to a later call. It is
	// opaque, and empty when nothing is pending.
	Mark string
}

// Pending is the partition's whole pending count.
func (p PendingSplit) Pending() int {
	return p.Through + p.Since
}

// SplitPending counts consumer's pending items on either side of mark
// in one aggregate, loading no item. A mark of "" places every item
// after it.
//
// The mark is the newest pending receipt. [Store.Enqueue] writes a new
// UUIDv7 receipt on every call, a coalescing one included, and the v7
// values one process makes sort in the order it made them. So an item
// whose receipt sorts after the mark was enqueued after the mark was
// taken. A consumer that records the mark when it acts on its partition
// can later tell the work it acted on, still waiting unchanged, from
// work that arrived since, without reading either. The receipts of
// items [Store.Append] added, and of rows seeded before receipts
// existed, do not sort this way, so a partition that holds them is not
// split meaningfully.
func (s *Store) SplitPending(ctx context.Context, consumer, mark string) (PendingSplit, error) {
	var (
		split PendingSplit
		total int
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(receipt <= ?), 0), COUNT(*), COALESCE(MAX(receipt), '')
		FROM loop_queue
		WHERE consumer_loop = ? AND status = ?
	`, mark, strings.TrimSpace(consumer), StatusPending).Scan(&split.Through, &total, &split.Mark)
	if err != nil {
		return PendingSplit{}, fmt.Errorf("loopqueue: split pending for %q: %w", consumer, err)
	}
	split.Since = total - split.Through
	return split, nil
}

// PendingCountsByKeyPrefix counts consumer's pending items by the prefix
// their key starts with, and all of them, in one aggregate that reads
// no payload. A producer that keys its items by what it counts, as in
// "draft:<account>:<id>", counts by it without the queue interpreting
// payloads, so the count costs the same however large they are.
//
// counts holds every non-empty prefix, zero included. A key that starts
// with more than one prefix counts under the longest, and a key that
// starts with none counts only in total.
func (s *Store) PendingCountsByKeyPrefix(ctx context.Context, consumer string, prefixes []string) (counts map[string]int, total int, err error) {
	counts = make(map[string]int, len(prefixes))
	ordered := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		if _, seen := counts[p]; p == "" || seen {
			continue
		}
		counts[p] = 0
		ordered = append(ordered, p)
	}
	// Longest first, so the CASE names the longest prefix a key has.
	slices.SortStableFunc(ordered, func(a, b string) int { return cmp.Compare(len(b), len(a)) })

	var group strings.Builder
	args := make([]any, 0, len(ordered)+2)
	group.WriteString("CASE")
	for i, p := range ordered {
		fmt.Fprintf(&group, " WHEN instr(dedup_key, ?) = 1 THEN %d", i)
		args = append(args, p)
	}
	group.WriteString(" END")
	if len(ordered) == 0 {
		group.Reset()
		group.WriteString("NULL")
	}
	args = append(args, strings.TrimSpace(consumer), StatusPending)

	rows, err := s.db.QueryContext(ctx, `
		SELECT `+group.String()+`, COUNT(*)
		FROM loop_queue
		WHERE consumer_loop = ? AND status = ?
		GROUP BY 1
	`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("loopqueue: pending counts by key prefix for %q: %w", consumer, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			prefix sql.NullInt64
			n      int
		)
		if err := rows.Scan(&prefix, &n); err != nil {
			return nil, 0, fmt.Errorf("loopqueue: pending counts by key prefix for %q: %w", consumer, err)
		}
		total += n
		if prefix.Valid && prefix.Int64 >= 0 && int(prefix.Int64) < len(ordered) {
			counts[ordered[prefix.Int64]] = n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("loopqueue: pending counts by key prefix for %q: %w", consumer, err)
	}
	return counts, total, nil
}
