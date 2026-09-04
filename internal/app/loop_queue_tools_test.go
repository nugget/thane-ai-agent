package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

func TestQueuePullAllowsOneBatchPerModelRequest(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"session:one", "session:two"} {
		if err := store.Enqueue(t.Context(), "archivist", subject, 0, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}

	tools := buildLoopQueueTools(store, "archivist")
	var pull func(context.Context, map[string]any) (string, error)
	for _, tool := range tools {
		if tool.Name == "queue_pull" {
			pull = tool.Handler
		}
	}
	if pull == nil {
		t.Fatal("queue_pull not generated")
	}

	firstCtx := logging.WithRequestID(t.Context(), "r_first")
	if _, err := pull(firstCtx, map[string]any{"limit": 1}); err != nil {
		t.Fatalf("first pull: %v", err)
	}
	if _, err := pull(firstCtx, map[string]any{"limit": 1}); err == nil || !strings.Contains(err.Error(), "next iteration") {
		t.Fatalf("second same-request pull error = %v, want iteration boundary", err)
	}
	secondCtx := logging.WithRequestID(t.Context(), "r_second")
	if _, err := pull(secondCtx, map[string]any{"limit": 1}); err != nil {
		t.Fatalf("next-request pull: %v", err)
	}
}

func TestArchivistQueuePullEnforcesWakeBatchLimit(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Enough to exercise the ceiling, which is now above the default.
	subjects := make([]string, 0, archivistPullMaxLimit+3)
	for i := range archivistPullMaxLimit + 3 {
		subjects = append(subjects, fmt.Sprintf("session:%02d", i))
	}
	for _, subject := range subjects {
		if err := store.Enqueue(t.Context(), "archivist", subject, 0, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}

	tools := buildLoopQueueTools(store, "archivist")
	var pull func(context.Context, map[string]any) (string, error)
	for _, tool := range tools {
		if tool.Name == "queue_pull" {
			pull = tool.Handler
		}
	}
	if pull == nil {
		t.Fatal("queue_pull not generated")
	}

	for _, tc := range []struct {
		name      string
		want      int
		requestID string
		args      map[string]any
	}{
		{name: "omitted limit takes the default", requestID: "r_default", args: map[string]any{}, want: archivistPullDefaultLimit},
		{
			// Previously this clamped to the default too, because the
			// default and the ceiling were the same number. They are not
			// any more: a loop that can see it is behind may ask for a
			// bigger batch, up to the ceiling.
			name:      "oversized limit clamps to the ceiling, not the default",
			requestID: "r_oversized", args: map[string]any{"limit": 25}, want: archivistPullMaxLimit,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := pull(logging.WithRequestID(t.Context(), tc.requestID), tc.args)
			if err != nil {
				t.Fatalf("queue_pull: %v", err)
			}
			var result queuePullResult
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatalf("decode queue_pull: %v", err)
			}
			if result.Count != tc.want || len(result.Items) != tc.want {
				t.Fatalf("queue_pull result count = %d (%d items), want %d", result.Count, len(result.Items), tc.want)
			}
		})
	}
}

func TestQueueEnqueueRejectsPulledSubject(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	const subject = "theme:not-yet"
	if err := store.Enqueue(t.Context(), "archivist", subject, 0, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	tools := buildLoopQueueTools(store, "archivist")
	var pull, enqueue func(context.Context, map[string]any) (string, error)
	for _, tool := range tools {
		switch tool.Name {
		case "queue_pull":
			pull = tool.Handler
		case "queue_enqueue":
			enqueue = tool.Handler
		}
	}
	if pull == nil || enqueue == nil {
		t.Fatal("queue pull/enqueue tools not generated")
	}
	if _, err := pull(t.Context(), map[string]any{"limit": 1}); err != nil {
		t.Fatalf("queue_pull: %v", err)
	}
	if _, err := enqueue(t.Context(), map[string]any{"subject": subject}); err == nil || !strings.Contains(err.Error(), "queue_ack or queue_defer") {
		t.Fatalf("queue_enqueue pulled subject error = %v, want completion guidance", err)
	}
}

func TestQueueDeferToolRetainsWorkAndMovesItBehindThePartition(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatalf("new loop queue: %v", err)
	}
	if err := store.Enqueue(t.Context(), "archivist", "contact:blocked", 5, []byte(`{"source":"session"}`)); err != nil {
		t.Fatalf("enqueue blocked: %v", err)
	}
	if err := store.Enqueue(t.Context(), "archivist", "session:ready", 0, []byte(`{}`)); err != nil {
		t.Fatalf("enqueue ready: %v", err)
	}

	tools := buildLoopQueueTools(store, "archivist")
	var deferToolNameFound bool
	for _, tool := range tools {
		if tool.Name != "queue_defer" {
			continue
		}
		deferToolNameFound = true
		result, err := tool.Handler(context.Background(), map[string]any{"subject": "contact:blocked"})
		if err != nil {
			t.Fatalf("queue_defer: %v", err)
		}
		var response map[string]string
		if err := json.Unmarshal([]byte(result), &response); err != nil {
			t.Fatalf("decode queue_defer result: %v", err)
		}
		if response["status"] != "deferred" || response["subject"] != "contact:blocked" {
			t.Fatalf("queue_defer result = %v", response)
		}
	}
	if !deferToolNameFound {
		t.Fatal("queue_defer runtime tool was not generated")
	}

	items, err := store.Peek(t.Context(), "archivist", 10)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if len(items) != 2 || items[0].DedupKey != "session:ready" || items[1].DedupKey != "contact:blocked" {
		t.Fatalf("deferred queue order = %#v", items)
	}
	if got := string(items[1].Payload); got != `{"source":"session"}` {
		t.Fatalf("deferred payload = %q", got)
	}
}

func TestQueueAckToolRetainsNewerCoalescedItem(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	const subject = "contact:changing"
	if err := store.Enqueue(t.Context(), "archivist", subject, 0, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}

	tools := buildLoopQueueTools(store, "archivist")
	var pull, ack func(context.Context, map[string]any) (string, error)
	for _, tool := range tools {
		switch tool.Name {
		case "queue_pull":
			pull = tool.Handler
		case "queue_ack":
			ack = tool.Handler
		}
	}
	if pull == nil || ack == nil {
		t.Fatal("queue pull/ack tools not generated")
	}
	if _, err := pull(t.Context(), map[string]any{"limit": 1}); err != nil {
		t.Fatalf("queue_pull: %v", err)
	}
	if err := store.Enqueue(t.Context(), "archivist", subject, 0, []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	result, err := ack(t.Context(), map[string]any{"subject": subject})
	if err != nil {
		t.Fatalf("queue_ack: %v", err)
	}
	var response map[string]string
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		t.Fatal(err)
	}
	if response["status"] != "retained_newer" {
		t.Fatalf("queue_ack response = %v, want retained_newer", response)
	}
	items, err := store.Peek(t.Context(), "archivist", 1)
	if err != nil || len(items) != 1 || string(items[0].Payload) != `{"v":2}` {
		t.Fatalf("newer item was not retained: items=%#v err=%v", items, err)
	}
}

func TestQueueAckToolDiscardsCompletedReceiptBeforeKeyRecreation(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	const subject = "contact:recreated"
	if err := store.Enqueue(t.Context(), "archivist", subject, 0, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}

	tools := buildLoopQueueTools(store, "archivist")
	var pull, ack func(context.Context, map[string]any) (string, error)
	for _, tool := range tools {
		switch tool.Name {
		case "queue_pull":
			pull = tool.Handler
		case "queue_ack":
			ack = tool.Handler
		}
	}
	if _, err := pull(t.Context(), map[string]any{"limit": 1}); err != nil {
		t.Fatal(err)
	}
	if result, err := ack(t.Context(), map[string]any{"subject": subject}); err != nil || !strings.Contains(result, `"status":"ok"`) {
		t.Fatalf("first ack result=%q err=%v", result, err)
	}
	if _, err := ack(t.Context(), map[string]any{"subject": subject}); err == nil || !strings.Contains(err.Error(), "no receipt from queue_pull") {
		t.Fatalf("repeated ack error = %v, want discarded receipt", err)
	}
	if err := store.Enqueue(t.Context(), "archivist", subject, 0, []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := ack(t.Context(), map[string]any{"subject": subject}); err == nil || !strings.Contains(err.Error(), "no receipt from queue_pull") {
		t.Fatalf("delayed ack after recreation error = %v, want no receipt", err)
	}
	items, err := store.Peek(t.Context(), "archivist", 1)
	if err != nil || len(items) != 1 || string(items[0].Payload) != `{"v":2}` {
		t.Fatalf("recreated item was not retained: items=%#v err=%v", items, err)
	}
}

// TestQueuePullReportsRemainingDepth pins the signal a self-paced loop
// needs and did not have. A pull that hands back three items looked
// identical whether the queue held three or three hundred, so a loop
// choosing its own sleep was choosing it blind — and the archivist's
// backlog grew to 266 behind a four-hour nap that looked entirely
// reasonable from where it was standing.
func TestQueuePullReportsRemainingDepth(t *testing.T) {
	pullOnce := func(t *testing.T, enqueued, limit int) queuePullResult {
		t.Helper()

		db, err := database.OpenMemory()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		store, err := loopqueue.NewStore(db, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := range enqueued {
			subject := fmt.Sprintf("session:%03d", i)
			if err := store.Enqueue(t.Context(), "archivist", subject, 0, []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
		}

		var pull func(context.Context, map[string]any) (string, error)
		for _, tool := range buildLoopQueueTools(store, "archivist") {
			if tool.Name == "queue_pull" {
				pull = tool.Handler
			}
		}
		raw, err := pull(logging.WithRequestID(t.Context(), "r1"), map[string]any{"limit": limit})
		if err != nil {
			t.Fatalf("pull: %v", err)
		}
		var got queuePullResult
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return got
	}

	tests := []struct {
		name          string
		enqueued      int
		limit         int
		wantCount     int
		wantRemaining int
	}{
		{
			// The case that motivated this: a full batch, and a great
			// deal more behind it.
			name: "a deep queue says how deep", enqueued: 266, limit: 3,
			wantCount: 3, wantRemaining: 263,
		},
		{
			// Peeked items stay pending until acked, so remaining has to
			// exclude the batch in hand or it would double-count it.
			name: "remaining excludes the batch in hand", enqueued: 5, limit: 5,
			wantCount: 5, wantRemaining: 0,
		},
		{name: "an exhausted queue reports nothing behind it", enqueued: 2, limit: 3, wantCount: 2, wantRemaining: 0},
		{name: "an empty queue", enqueued: 0, limit: 3, wantCount: 0, wantRemaining: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pullOnce(t, tt.enqueued, tt.limit)
			if got.Count != tt.wantCount {
				t.Errorf("count = %d, want %d", got.Count, tt.wantCount)
			}
			if got.Remaining == nil {
				t.Fatalf("remaining = null, want it measured as %d", tt.wantRemaining)
			}
			if *got.Remaining != tt.wantRemaining {
				t.Errorf("remaining = %d, want %d", *got.Remaining, tt.wantRemaining)
			}
			// Depth alone cannot tell a burst that just arrived from a
			// backlog losing ground for days, and those want different
			// sleeps — so the wait is reported whenever anything remains.
			if tt.wantRemaining > 0 && got.OldestWait == "" {
				t.Error("remaining work reported with no oldest_wait")
			}
			if tt.wantRemaining == 0 && got.OldestWait != "" {
				t.Errorf("oldest_wait = %q with nothing remaining", got.OldestWait)
			}
		})
	}
}

// TestArchivistPullCeilingExceedsItsDefault pins that a backlog can
// actually be worked through. The default and the maximum were the same
// number, so no prompt could ask for a bigger batch and a queue could
// only ever drain three subjects per wake however far behind it fell.
// The default is unchanged: steady state should still be three careful
// subjects, not twelve rushed ones.
func TestArchivistPullCeilingExceedsItsDefault(t *testing.T) {
	def, max := queuePullLimits("archivist")
	if def != archivistPullDefaultLimit {
		t.Errorf("default = %d, want %d", def, archivistPullDefaultLimit)
	}
	if max <= def {
		t.Errorf("max = %d and default = %d; a backlog cannot be worked down when they are equal", max, def)
	}
	// Each subject pulls a session transcript into context. A ceiling
	// large enough to push one call past a local model's window would
	// route that turn to a cloud provider, which fixes the backlog by
	// quietly paying more for it.
	if max > queuePullMaxLimit {
		t.Errorf("max = %d exceeds the general ceiling %d", max, queuePullMaxLimit)
	}
}

// TestQueuePullOldestWaitDescribesWhatIsLeft pins that the reported age
// belongs to the backlog, not to the batch in hand.
//
// Drain order is oldest-first within a priority, so an aggregate over the
// whole partition necessarily returns an item this pull just handed over.
// A loop reading that would size its sleep against the age of work it had
// already picked up — precisely backwards.
func TestQueuePullOldestWaitDescribesWhatIsLeft(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Four items, of which the three that a batch would take are made
	// genuinely older. Enqueueing in a loop is not enough: the rows land
	// inside the same second and their timestamps tie, which hides the
	// very difference this test exists to detect.
	for i := range 4 {
		if err := store.Enqueue(t.Context(), "archivist", fmt.Sprintf("session:%d", i), 0, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(t.Context(),
		`UPDATE loop_queue SET enqueued_at = datetime(enqueued_at, '-1 hour')
		 WHERE dedup_key IN ('session:0','session:1','session:2')`); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	pending, oldest, err := store.PendingBeyond(t.Context(), "archivist", 3)
	if err != nil {
		t.Fatalf("PendingBeyond: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending beyond 3 = %d, want 1", pending)
	}

	all, wholePartitionOldest, err := store.PendingBeyond(t.Context(), "archivist", 0)
	if err != nil {
		t.Fatalf("PendingBeyond(0): %v", err)
	}
	if all != 4 {
		t.Fatalf("pending beyond 0 = %d, want 4", all)
	}
	if !oldest.After(wholePartitionOldest) {
		t.Errorf("oldest beyond the batch (%s) is not newer than the partition's oldest (%s); the batch in hand is being described",
			oldest, wholePartitionOldest)
	}
}

// TestQueuePullUnmeasuredDepthIsNullNotZero pins the result shape for a
// failed probe. Reporting 0 would be indistinguishable from a drained
// queue — the same absent-signal-reads-as-healthy failure this whole
// change exists to remove, one layer down.
func TestQueuePullUnmeasuredDepthIsNullNotZero(t *testing.T) {
	unmeasured, err := json.Marshal(queuePullResult{Count: 1, Items: []queueItemView{{Subject: "session:x"}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(unmeasured), `"remaining":null`) {
		t.Errorf("unmeasured depth serialized as %s, want an explicit null", unmeasured)
	}

	zero := 0
	measured, err := json.Marshal(queuePullResult{Count: 1, Remaining: &zero, Items: []queueItemView{{Subject: "session:x"}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(measured), `"remaining":0`) {
		t.Errorf("a measured empty queue serialized as %s, want remaining 0", measured)
	}
}
