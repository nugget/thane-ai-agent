package loopqueue

import (
	"context"
	"testing"
	"time"
)

func completionCount(t *testing.T, s *Store, consumer string) int {
	t.Helper()
	var n int
	query := `SELECT COUNT(*) FROM loop_queue_completions`
	args := []any{}
	if consumer != "" {
		query += ` WHERE consumer_loop = ?`
		args = append(args, consumer)
	}
	if err := s.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count completions: %v", err)
	}
	return n
}

func TestAckJournalsExactlyOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	if err := s.Enqueue(ctx, "archivist", "session:a", 3, []byte(`{}`)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	generation := ackPending(t, s, "archivist", "session:a")
	if got := completionCount(t, s, "archivist"); got != 1 {
		t.Fatalf("completions after ack = %d, want 1", got)
	}

	// Re-acking the same key is idempotent for the queue AND for the
	// journal: no phantom completion rows, no double-counted throughput.
	if outcome, err := s.Ack(ctx, "archivist", "session:a", generation); err != nil || outcome != AckMissing {
		t.Fatalf("re-ack outcome=%q err=%v", outcome, err)
	}
	if got := completionCount(t, s, "archivist"); got != 1 {
		t.Errorf("completions after re-ack = %d, want 1", got)
	}

	comps, err := s.RecentCompletions(ctx, "archivist", time.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatalf("recent completions: %v", err)
	}
	if len(comps) != 1 {
		t.Fatalf("recent completions = %d rows, want 1", len(comps))
	}
	c := comps[0]
	if c.Consumer != "archivist" || c.DedupKey != "session:a" {
		t.Errorf("completion = %+v, want archivist/session:a", c)
	}
	if c.EnqueuedAt.IsZero() || c.AckedAt.IsZero() {
		t.Errorf("completion timestamps not recorded: %+v", c)
	}
	if c.Waited() < 0 {
		t.Errorf("waited = %v, want >= 0", c.Waited())
	}
}

// TestCoalesceIsNotACompletion pins the deliberate decision: a
// re-Enqueue on the same dedup key refreshes the pending item in place
// and must not appear in the journal — otherwise a chatty producer
// reads as consumer throughput.
func TestCoalesceIsNotACompletion(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	for range 5 {
		if err := s.Enqueue(ctx, "archivist", "entity:sensor.x", 0, []byte(`{}`)); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	if got := completionCount(t, s, "archivist"); got != 0 {
		t.Errorf("completions after coalescing enqueues = %d, want 0", got)
	}
	if n, _ := s.PendingCount(ctx, "archivist"); n != 1 {
		t.Errorf("pending after coalescing enqueues = %d, want 1", n)
	}
}

func TestPendingStatsGroupsAndFindsOldest(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	if err := s.Enqueue(ctx, "archivist", "session:1", 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, "archivist", "session:2", 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "core", "signal", 0, nil); err != nil {
		t.Fatal(err)
	}

	stats, err := s.PendingStats(ctx)
	if err != nil {
		t.Fatalf("pending stats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("stats = %+v, want 2 partitions", stats)
	}
	// Sorted by consumer name: archivist before core.
	if stats[0].Consumer != "archivist" || stats[0].Pending != 2 {
		t.Errorf("stats[0] = %+v, want archivist pending 2", stats[0])
	}
	if stats[1].Consumer != "core" || stats[1].Pending != 1 {
		t.Errorf("stats[1] = %+v, want core pending 1", stats[1])
	}
	if stats[0].OldestEnqueuedAt.IsZero() {
		t.Errorf("oldest enqueued_at not parsed")
	}

	counts, err := s.PendingCounts(ctx)
	if err != nil {
		t.Fatalf("pending counts: %v", err)
	}
	if counts["archivist"] != 2 || counts["core"] != 1 {
		t.Errorf("counts = %v, want archivist:2 core:1", counts)
	}
}

func TestCompletionStatsAggregatesPerConsumer(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	for _, key := range []string{"session:1", "session:2"} {
		if err := s.Enqueue(ctx, "archivist", key, 0, nil); err != nil {
			t.Fatal(err)
		}
		ackPending(t, s, "archivist", key)
	}

	stats, err := s.CompletionStatsSince(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("completion stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats = %+v, want 1 consumer", stats)
	}
	if stats[0].Consumer != "archivist" || stats[0].Count != 2 {
		t.Errorf("stats[0] = %+v, want archivist count 2", stats[0])
	}
	if stats[0].AvgWait < 0 || stats[0].MaxWait < stats[0].AvgWait {
		t.Errorf("wait math inconsistent: avg=%v max=%v", stats[0].AvgWait, stats[0].MaxWait)
	}

	// A window that starts after the acks sees nothing.
	later, err := s.CompletionStatsSince(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("completion stats (future window): %v", err)
	}
	if len(later) != 0 {
		t.Errorf("future-window stats = %+v, want none", later)
	}
}

func TestPruneCompletionsIsAgeBased(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	if err := s.Enqueue(ctx, "archivist", "session:old", 0, nil); err != nil {
		t.Fatal(err)
	}
	ackPending(t, s, "archivist", "session:old")
	// Backdate the journal row two days so a one-day retention removes it.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE loop_queue_completions SET acked_at = ? WHERE dedup_key = 'session:old'`,
		time.Now().UTC().Add(-48*time.Hour),
	); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	deleted, err := s.PruneCompletions(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 1 {
		t.Errorf("pruned %d rows, want 1", deleted)
	}
	if got := completionCount(t, s, ""); got != 0 {
		t.Errorf("completions after prune = %d, want 0", got)
	}

	if _, err := s.PruneCompletions(ctx, 0); err == nil {
		t.Errorf("prune with zero max age must refuse rather than delete everything")
	}
}
