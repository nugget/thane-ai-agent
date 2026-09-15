package email

import (
	"context"
	"encoding/json"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

// heapAllocated reports how many bytes f allocated on the Go heap.
func heapAllocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// ackReview acknowledges subject in the review queue under its current
// receipt.
func ackReview(t *testing.T, queue *loopqueue.Store, subject string) {
	t.Helper()
	items, err := queue.PeekAll(context.Background(), testReviewLoop)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	for _, item := range items {
		if item.DedupKey != subject {
			continue
		}
		if _, err := queue.Ack(context.Background(), testReviewLoop, subject, item.Receipt); err != nil {
			t.Fatalf("ack %s: %v", subject, err)
		}
		return
	}
	t.Fatalf("%s is not pending", subject)
}

// TestPendingReviewCountFollowsTheQueue pins the per-account count, and
// the cached count the Email Accounts entry renders, through every way
// the review queue changes: an enqueue adds one, a coalescing enqueue
// and a deferral change nothing, an item whose subject names no account
// is not the account's, and an acknowledgement removes one. The count
// follows the subject and never the payload: an item keyed for the
// account counts whatever its payload says, and an item keyed for
// another account does not, whatever its payload says.
func TestPendingReviewCountFollowsTheQueue(t *testing.T) {
	f, queue := reviewFixture(t, testReviewLoop, nil)
	ctx := context.Background()
	draft := draftReviewSubject("primary", "d-1")
	message := messageReviewSubject("primary", "<m@example.com>")
	blank := draftReviewSubject("primary", "d-blank")
	enqueueRaw := func(subject, payload string) func(*testing.T) {
		return func(t *testing.T) {
			t.Helper()
			if err := queue.Enqueue(ctx, testReviewLoop, subject, 0, []byte(payload)); err != nil {
				t.Fatalf("enqueue %s: %v", subject, err)
			}
		}
	}
	enqueue := func(subject, kind string) func(*testing.T) {
		return func(t *testing.T) {
			t.Helper()
			if _, err := f.svc.enqueueReview(ctx, testReviewLoop, "primary", subject, kind, "{}"); err != nil {
				t.Fatalf("enqueue %s: %v", subject, err)
			}
		}
	}

	steps := []struct {
		name   string
		change func(*testing.T)
		want   int
	}{
		{"a draft", enqueue(draft, reviewTypeDraft), 1},
		{"an escalated message", enqueue(message, reviewTypeMessage), 2},
		{"the draft queued again", enqueue(draft, reviewTypeDraft), 2},
		{"the draft deferred", func(t *testing.T) {
			if err := queue.Defer(ctx, testReviewLoop, draft); err != nil {
				t.Fatalf("defer: %v", err)
			}
		}, 2},
		{"an item whose subject names no account", enqueueRaw("foreign", `{}`), 2},
		{"an item keyed for the account, its payload blank", enqueueRaw(blank, `{}`), 3},
		{"an item keyed for another account, its payload naming this one", enqueueRaw(draftReviewSubject("work", "d-9"),
			`{"events":[{"source":"email_review","type":"draft","metadata":{"account":"primary"}}]}`), 3},
		{"the blank item acknowledged", func(t *testing.T) { ackReview(t, queue, blank) }, 2},
		{"the message acknowledged", func(t *testing.T) { ackReview(t, queue, message) }, 1},
		{"the draft acknowledged", func(t *testing.T) { ackReview(t, queue, draft) }, 0},
	}
	for _, step := range steps {
		step.change(t)
		counts, err := f.svc.measurePendingReview(ctx, testReviewLoop)
		if err != nil {
			t.Fatalf("%s: count: %v", step.name, err)
		}
		if counts["primary"] != step.want {
			t.Fatalf("%s: counted %d for primary, want %d", step.name, counts["primary"], step.want)
		}
		if c, ok := f.svc.cachedPendingReview("primary"); !ok || c.Pending != step.want {
			t.Fatalf("%s: cached count = %+v ok=%v, want %d", step.name, c, ok, step.want)
		}
	}
}

// TestPendingReviewCountLoadsNoItem pins that counting a backlog, after
// an enqueue or on its own, costs no more memory than counting an empty
// queue. A count that loaded the partition would allocate at least the
// backlog's size, twice over, on every enqueue and every poll.
func TestPendingReviewCountLoadsNoItem(t *testing.T) {
	const (
		backlog      = 32
		summaryBytes = 128 << 10 // a 4 MiB backlog
		bound        = 1 << 20
	)
	f, queue := reviewFixture(t, testReviewLoop, nil)
	ctx := context.Background()
	summary := strings.Repeat("x", summaryBytes)
	for i := range backlog {
		raw, err := json.Marshal(messages.LoopNotifyPayload{Events: []messages.LoopEventPayload{{
			Source:   reviewSource,
			Type:     reviewTypeDraft,
			Summary:  summary,
			Metadata: map[string]string{"account": "primary"},
		}}})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := queue.Enqueue(ctx, testReviewLoop, draftReviewSubject("primary", strconv.Itoa(i)), 0, raw); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	var (
		counts map[string]int
		err    error
	)
	if n := heapAllocated(func() { counts, err = f.svc.measurePendingReview(ctx, testReviewLoop) }); n > bound {
		t.Errorf("counting a 4 MiB backlog allocated %d bytes: the count loaded the queue", n)
	}
	if err != nil || counts["primary"] != backlog {
		t.Fatalf("count = %v, %v; want %d for primary", counts, err, backlog)
	}

	var pending *int
	if n := heapAllocated(func() {
		pending, err = f.svc.enqueueReview(ctx, testReviewLoop, "primary", draftReviewSubject("primary", "one-more"), reviewTypeDraft, "{}")
	}); n > bound {
		t.Errorf("an enqueue behind a 4 MiB backlog allocated %d bytes: its count loaded the queue", n)
	}
	if err != nil || pending == nil || *pending != backlog+1 {
		t.Fatalf("enqueue = %v, %v; want %d pending", pending, err, backlog+1)
	}
}
