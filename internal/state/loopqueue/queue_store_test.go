package loopqueue

import (
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"

	_ "github.com/mattn/go-sqlite3"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func TestStore_EnqueuePeekAck(t *testing.T) {
	s := newTestStore(t)

	if err := s.Enqueue("archivist", "session:abc", 0, []byte(`{"kind":"k"}`)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	items, err := s.Peek("archivist", 10)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("peek returned %d items, want 1", len(items))
	}
	if items[0].DedupKey != "session:abc" {
		t.Errorf("dedup_key = %q, want session:abc", items[0].DedupKey)
	}
	if string(items[0].Payload) != `{"kind":"k"}` {
		t.Errorf("payload = %q", items[0].Payload)
	}
	if items[0].EnqueuedAt.IsZero() {
		t.Errorf("enqueued_at not parsed")
	}

	if err := s.Ack("archivist", "session:abc"); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if n, _ := s.PendingCount("archivist"); n != 0 {
		t.Errorf("pending after ack = %d, want 0", n)
	}
	// Ack of a missing key is a no-op, not an error.
	if err := s.Ack("archivist", "session:gone"); err != nil {
		t.Errorf("ack missing: %v", err)
	}
}

func TestStore_CoalesceOnDedupKey(t *testing.T) {
	s := newTestStore(t)

	if err := s.Enqueue("archivist", "entity:foo", 1, []byte(`{"v":1}`)); err != nil {
		t.Fatalf("enqueue 1: %v", err)
	}
	// Re-enqueue same key with higher priority + new payload: coalesce.
	if err := s.Enqueue("archivist", "entity:foo", 5, []byte(`{"v":2}`)); err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}
	// Re-enqueue with lower priority must not demote.
	if err := s.Enqueue("archivist", "entity:foo", 2, []byte(`{"v":3}`)); err != nil {
		t.Fatalf("enqueue 3: %v", err)
	}

	if n, _ := s.PendingCount("archivist"); n != 1 {
		t.Fatalf("pending = %d, want 1 (coalesced)", n)
	}
	items, err := s.Peek("archivist", 10)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if got := items[0].Priority; got != 5 {
		t.Errorf("priority = %d, want 5 (MAX of 1,5,2)", got)
	}
	if string(items[0].Payload) != `{"v":3}` {
		t.Errorf("payload = %q, want latest {\"v\":3}", items[0].Payload)
	}
}

func TestStore_PriorityOrdering(t *testing.T) {
	s := newTestStore(t)
	for _, tc := range []struct {
		key  string
		prio int
	}{{"a", 0}, {"b", 5}, {"c", 2}} {
		if err := s.Enqueue("archivist", tc.key, tc.prio, nil); err != nil {
			t.Fatalf("enqueue %s: %v", tc.key, err)
		}
	}
	items, err := s.Peek("archivist", 10)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	gotOrder := []string{items[0].DedupKey, items[1].DedupKey, items[2].DedupKey}
	want := []string{"b", "c", "a"} // priority 5, 2, 0
	for i := range want {
		if gotOrder[i] != want[i] {
			t.Errorf("drain order = %v, want %v", gotOrder, want)
			break
		}
	}
}

func TestStore_PartitionIsolation(t *testing.T) {
	s := newTestStore(t)
	if err := s.Enqueue("archivist", "session:1", 0, nil); err != nil {
		t.Fatalf("enqueue archivist: %v", err)
	}
	if err := s.Enqueue("mqtt-sec", "topic:alarm", 0, nil); err != nil {
		t.Fatalf("enqueue mqtt: %v", err)
	}
	// Same dedup_key in a different partition is a distinct item.
	if err := s.Enqueue("mqtt-sec", "session:1", 0, nil); err != nil {
		t.Fatalf("enqueue mqtt dup-key: %v", err)
	}

	if n, _ := s.PendingCount("archivist"); n != 1 {
		t.Errorf("archivist pending = %d, want 1", n)
	}
	if n, _ := s.PendingCount("mqtt-sec"); n != 2 {
		t.Errorf("mqtt-sec pending = %d, want 2", n)
	}
	items, _ := s.Peek("archivist", 10)
	if len(items) != 1 || items[0].DedupKey != "session:1" {
		t.Errorf("archivist peek leaked another partition: %+v", items)
	}
}
