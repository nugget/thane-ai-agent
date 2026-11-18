package loopqueue

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func TestRecordDeliverySurvivesReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "q.db")
	ctx := context.Background()
	open := func() (*Store, func()) {
		db, err := database.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		s, err := NewStore(db, nil)
		if err != nil {
			t.Fatal(err)
		}
		return s, func() { _ = db.Close() }
	}
	s, closeDB := open()
	if _, err := s.Append(ctx, "c", "m", 0, []byte("p")); err != nil {
		t.Fatal(err)
	}
	closeDB()
	for boot := 1; boot <= 3; boot++ {
		s, closeDB := open()
		items, err := s.Peek(ctx, "c", 16)
		if err != nil || len(items) != 1 || items[0].Attempts != boot-1 {
			t.Fatalf("boot %d peek = %+v, %v; want attempts %d", boot, items, err, boot-1)
		}
		if err := s.RecordDelivery(ctx, "c", items); err != nil {
			t.Fatal(err)
		}
		closeDB() // the process dies without an ack
	}
	s, closeDB = open()
	defer closeDB()
	items, _ := s.Peek(ctx, "c", 16)
	if len(items) != 1 || items[0].Attempts != 3 {
		t.Fatalf("after 3 boots: %+v, want attempts 3", items)
	}
}

func TestRefundDelivery(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Append(ctx, "c", "m", 0, []byte("p")); err != nil {
		t.Fatal(err)
	}
	items, _ := s.Peek(ctx, "c", 1)
	if err := s.RecordDelivery(ctx, "c", items); err != nil {
		t.Fatal(err)
	}
	for range 2 { // floors at zero
		if err := s.RefundDelivery(ctx, "c", items); err != nil {
			t.Fatal(err)
		}
	}
	stale := []Item{{DedupKey: items[0].DedupKey, Receipt: "other"}}
	if err := s.RecordDelivery(ctx, "c", stale); err != nil { // receipt mismatch: skipped
		t.Fatal(err)
	}
	got, _ := s.Peek(ctx, "c", 1)
	if got[0].Attempts != 0 {
		t.Fatalf("attempts = %d, want 0", got[0].Attempts)
	}
}

func TestPutAsideHidesFromPendingReaders(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	key, err := s.Append(ctx, "signal/a", "m", 0, []byte("p"))
	if err != nil {
		t.Fatal(err)
	}
	items, _ := s.Peek(ctx, "signal/a", 1)
	if n, err := s.PutAside(ctx, "signal/a", items); err != nil || n != 1 {
		t.Fatalf("PutAside = %d, %v", n, err)
	}
	if n, err := s.PutAside(ctx, "signal/a", items); err != nil || n != 0 {
		t.Fatalf("second PutAside = %d, %v; want 0 changed", n, err)
	}
	checks := map[string]func() int{
		"Peek":             func() int { it, _ := s.PeekAll(ctx, "signal/a"); return len(it) },
		"PendingCount":     func() int { n, _ := s.PendingCount(ctx, "signal/a"); return n },
		"PendingStats":     func() int { st, _ := s.PendingStats(ctx); return len(st) },
		"PendingConsumers": func() int { c, _ := s.PendingConsumers(ctx, "signal/"); return len(c) },
		"Consumers":        func() int { c, _ := s.Consumers(ctx, "signal/"); return len(c) },
	}
	for name, f := range checks {
		if got := f(); got != 0 {
			t.Errorf("%s sees the set-aside row (%d)", name, got)
		}
	}
	if err := s.MoveConsumer(ctx, "signal/a", "signal/b"); err != nil {
		t.Fatal(err)
	}
	if outcome, err := s.Ack(ctx, "signal/a", key, key); err != nil || outcome != AckMissing {
		t.Fatalf("Ack of set-aside row = %q, %v; want missing", outcome, err)
	}
	aside, err := s.CountSetAside(ctx, time.Now().Add(-time.Minute))
	if err != nil || len(aside) != 1 || aside[0].Consumer != "signal/a" || aside[0].Count != 1 {
		t.Fatalf("CountSetAside = %+v, %v; want the row kept under signal/a", aside, err)
	}
	if aside, _ := s.CountSetAside(ctx, time.Now().Add(time.Minute)); len(aside) != 0 {
		t.Fatalf("CountSetAside(future) = %+v, want none", aside)
	}
}
