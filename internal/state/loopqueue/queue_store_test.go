package loopqueue

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"

	_ "modernc.org/sqlite"
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

	if err := s.Enqueue(t.Context(), "archivist", "session:abc", 0, []byte(`{"kind":"k"}`)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	items, err := s.Peek(t.Context(), "archivist", 10)
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

	if err := s.Ack(t.Context(), "archivist", "session:abc"); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if n, _ := s.PendingCount(t.Context(), "archivist"); n != 0 {
		t.Errorf("pending after ack = %d, want 0", n)
	}
	// Ack of a missing key is a no-op, not an error.
	if err := s.Ack(t.Context(), "archivist", "session:gone"); err != nil {
		t.Errorf("ack missing: %v", err)
	}
}

func TestStore_AppendKeepsDuplicateItems(t *testing.T) {
	s := newTestStore(t)

	first, err := s.Append(t.Context(), "signal/contact", "signal", 0, []byte(`{"message":"first"}`))
	if err != nil {
		t.Fatalf("append first: %v", err)
	}
	second, err := s.Append(t.Context(), "signal/contact", "signal", 0, []byte(`{"message":"second"}`))
	if err != nil {
		t.Fatalf("append second: %v", err)
	}
	if first == second {
		t.Fatalf("append generated duplicate key %q", first)
	}

	if n, _ := s.PendingCount(t.Context(), "signal/contact"); n != 2 {
		t.Fatalf("pending = %d, want 2", n)
	}
	items, err := s.PeekAll(t.Context(), "signal/contact")
	if err != nil {
		t.Fatalf("peek all: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if got := string(items[0].Payload); got != `{"message":"first"}` {
		t.Fatalf("first payload = %q", got)
	}
	if got := string(items[1].Payload); got != `{"message":"second"}` {
		t.Fatalf("second payload = %q", got)
	}
}

func TestStore_PendingConsumersAndMoveConsumer(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.Append(t.Context(), "signal/old", "signal", 0, []byte(`{"message":"one"}`)); err != nil {
		t.Fatalf("append old: %v", err)
	}
	if _, err := s.Append(t.Context(), "archivist", "item", 0, []byte(`{"message":"two"}`)); err != nil {
		t.Fatalf("append archivist: %v", err)
	}
	consumers, err := s.PendingConsumers(t.Context(), "signal/")
	if err != nil {
		t.Fatalf("PendingConsumers: %v", err)
	}
	if len(consumers) != 1 || consumers[0] != "signal/old" {
		t.Fatalf("consumers = %#v, want signal/old", consumers)
	}
	if err := s.MoveConsumer(t.Context(), "signal/old", "signal/new"); err != nil {
		t.Fatalf("MoveConsumer: %v", err)
	}
	if n, _ := s.PendingCount(t.Context(), "signal/old"); n != 0 {
		t.Fatalf("old pending = %d, want 0", n)
	}
	if n, _ := s.PendingCount(t.Context(), "signal/new"); n != 1 {
		t.Fatalf("new pending = %d, want 1", n)
	}
}

func TestStore_PendingConsumersEscapesLikeWildcards(t *testing.T) {
	s := newTestStore(t)

	// "signal/a_b" contains a LIKE wildcard ('_' = any single char).
	// Unescaped, the prefix query would also match "signal/axb"; the
	// prefix must be treated literally.
	if _, err := s.Append(t.Context(), "signal/a_b", "signal", 0, []byte(`{}`)); err != nil {
		t.Fatalf("append literal: %v", err)
	}
	if _, err := s.Append(t.Context(), "signal/axb", "signal", 0, []byte(`{}`)); err != nil {
		t.Fatalf("append wildcard-collision: %v", err)
	}
	consumers, err := s.PendingConsumers(t.Context(), "signal/a_b")
	if err != nil {
		t.Fatalf("PendingConsumers: %v", err)
	}
	if len(consumers) != 1 || consumers[0] != "signal/a_b" {
		t.Fatalf("consumers = %#v, want [signal/a_b] (literal prefix, not wildcard match)", consumers)
	}
}

func TestStore_CoalesceOnDedupKey(t *testing.T) {
	s := newTestStore(t)

	if err := s.Enqueue(t.Context(), "archivist", "entity:foo", 1, []byte(`{"v":1}`)); err != nil {
		t.Fatalf("enqueue 1: %v", err)
	}
	// Re-enqueue same key with higher priority + new payload: coalesce.
	if err := s.Enqueue(t.Context(), "archivist", "entity:foo", 5, []byte(`{"v":2}`)); err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}
	// Re-enqueue with lower priority must not demote.
	if err := s.Enqueue(t.Context(), "archivist", "entity:foo", 2, []byte(`{"v":3}`)); err != nil {
		t.Fatalf("enqueue 3: %v", err)
	}

	if n, _ := s.PendingCount(t.Context(), "archivist"); n != 1 {
		t.Fatalf("pending = %d, want 1 (coalesced)", n)
	}
	items, err := s.Peek(t.Context(), "archivist", 10)
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
		if err := s.Enqueue(t.Context(), "archivist", tc.key, tc.prio, nil); err != nil {
			t.Fatalf("enqueue %s: %v", tc.key, err)
		}
	}
	items, err := s.Peek(t.Context(), "archivist", 10)
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
	if err := s.Enqueue(t.Context(), "archivist", "session:1", 0, nil); err != nil {
		t.Fatalf("enqueue archivist: %v", err)
	}
	if err := s.Enqueue(t.Context(), "mqtt-sec", "topic:alarm", 0, nil); err != nil {
		t.Fatalf("enqueue mqtt: %v", err)
	}
	// Same dedup_key in a different partition is a distinct item.
	if err := s.Enqueue(t.Context(), "mqtt-sec", "session:1", 0, nil); err != nil {
		t.Fatalf("enqueue mqtt dup-key: %v", err)
	}

	if n, _ := s.PendingCount(t.Context(), "archivist"); n != 1 {
		t.Errorf("archivist pending = %d, want 1", n)
	}
	if n, _ := s.PendingCount(t.Context(), "mqtt-sec"); n != 2 {
		t.Errorf("mqtt-sec pending = %d, want 2", n)
	}
	items, _ := s.Peek(t.Context(), "archivist", 10)
	if len(items) != 1 || items[0].DedupKey != "session:1" {
		t.Errorf("archivist peek leaked another partition: %+v", items)
	}
}

// TestWakeOnEnqueueDebounce covers the #1033 seam: a burst of
// enqueues coalesces into one debounced wake, a lone enqueue fires
// after the trailing window, and consumers without a registration
// (the archivist posture) never fire.
func TestWakeOnEnqueueDebounce(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	fired := make(chan struct{}, 8)
	store.SetWakeOnEnqueue("mqtt-dispatch", 40*time.Millisecond, 500*time.Millisecond, func() {
		fired <- struct{}{}
	})

	// A burst inside the debounce window → exactly one wake.
	for i := 0; i < 5; i++ {
		if err := store.Enqueue(ctx, "mqtt-dispatch", fmt.Sprintf("mqtt:topic/%d", i), 0, []byte(`{}`)); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("debounced wake never fired after burst")
	}
	select {
	case <-fired:
		t.Fatal("burst fired more than one wake")
	case <-time.After(150 * time.Millisecond):
	}

	// A fresh enqueue after the burst starts a new window and fires again.
	if err := store.Enqueue(ctx, "mqtt-dispatch", "mqtt:topic/later", 0, []byte(`{}`)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("second burst never fired")
	}

	// An unregistered consumer stays silent.
	if err := store.Enqueue(ctx, "archivist", "session:x", 0, []byte(`{}`)); err != nil {
		t.Fatalf("enqueue archivist: %v", err)
	}
	select {
	case <-fired:
		t.Fatal("unregistered consumer fired the wrong consumer's wake")
	case <-time.After(150 * time.Millisecond):
	}
}

// TestWakeOnEnqueueMaxWait pins the anti-starvation bound: continuous
// chatter that keeps resetting the trailing window still fires no
// later than maxWait after the burst began.
func TestWakeOnEnqueueMaxWait(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	fired := make(chan time.Time, 4)
	store.SetWakeOnEnqueue("mqtt-dispatch", 60*time.Millisecond, 200*time.Millisecond, func() {
		fired <- time.Now()
	})

	start := time.Now()
	deadline := start.Add(600 * time.Millisecond)
	var firedAt time.Time
chatter:
	for time.Now().Before(deadline) {
		if err := store.Enqueue(ctx, "mqtt-dispatch", "mqtt:chatty", 0, []byte(`{}`)); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		select {
		case firedAt = <-fired:
			break chatter
		case <-time.After(25 * time.Millisecond):
		}
	}
	if firedAt.IsZero() {
		t.Fatal("chatter starved the wake past maxWait")
	}
	if elapsed := firedAt.Sub(start); elapsed > 450*time.Millisecond {
		t.Errorf("wake fired %v after burst start, want ≲ maxWait+debounce slack", elapsed)
	}
}

// TestSetWakeOnEnqueueNilRemoves verifies deregistration: a nil fire
// removes the hook and stops any pending timer.
func TestSetWakeOnEnqueueNilRemoves(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	fired := make(chan struct{}, 1)
	store.SetWakeOnEnqueue("mqtt-dispatch", 50*time.Millisecond, time.Second, func() {
		fired <- struct{}{}
	})
	if err := store.Enqueue(ctx, "mqtt-dispatch", "mqtt:x", 0, []byte(`{}`)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	store.SetWakeOnEnqueue("mqtt-dispatch", 0, 0, nil)
	select {
	case <-fired:
		t.Fatal("wake fired after deregistration")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestWakeOnEnqueueArmFireRace hammers the arm path with concurrent
// enqueues against a tiny debounce so timer expiry constantly races
// re-arming — the window where Reset-on-AfterFunc could double-fire
// (Copilot #1216). The generation guard must keep the count sane and
// the state clean: fires never exceed enqueues, at least one fires,
// and once traffic stops the count goes quiet (no stale late fires).
func TestWakeOnEnqueueArmFireRace(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	var fires atomic.Int64
	store.SetWakeOnEnqueue("racy", time.Millisecond, 5*time.Millisecond, func() {
		fires.Add(1)
	})

	const workers, perWorker = 4, 50
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if err := store.Enqueue(ctx, "racy", fmt.Sprintf("k:%d:%d", w, i), 0, []byte(`{}`)); err != nil {
					t.Errorf("enqueue: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	// Let the final burst settle, then confirm the count is stable.
	time.Sleep(50 * time.Millisecond)
	settled := fires.Load()
	if settled < 1 {
		t.Fatal("no wake fired at all")
	}
	if settled > workers*perWorker {
		t.Fatalf("fires = %d exceeds enqueues — duplicate callbacks leaked", settled)
	}
	time.Sleep(100 * time.Millisecond)
	if again := fires.Load(); again != settled {
		t.Fatalf("stale late fire: count moved %d → %d after quiet", settled, again)
	}
}
