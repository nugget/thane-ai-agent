package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

// syncBuffer is a log sink the waker's workers can write while a test
// reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitForLog reports whether a log line holding every part appears
// within timeout.
func waitForLog(logs *syncBuffer, timeout time.Duration, parts ...string) bool {
	deadline := time.Now().Add(timeout)
	for {
		for _, line := range strings.Split(logs.String(), "\n") {
			found := true
			for _, part := range parts {
				found = found && strings.Contains(line, part)
			}
			if found {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// emailPassQueueDB is emailPassQueue with the queue's database. The
// database has one connection, so a test that holds it in a transaction
// makes every queue read block until its context ends.
func emailPassQueueDB(t *testing.T) (*loopqueue.Store, *sql.DB) {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	queue, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatalf("new loopqueue store: %v", err)
	}
	return queue, db
}

// heapAllocated reports how many bytes f allocated on the Go heap.
func heapAllocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestEmailReviewRechecksLeftoverWorkWithoutPolling pins the recheck
// with polling off. The boot sweep wakes the review loop. An item the
// loop deferred wakes it again, with no poll to prompt it, and no sooner
// than review_delay after the last wake. Once the queue is empty, no
// recheck stays armed and no further wake comes.
func TestEmailReviewRechecksLeftoverWorkWithoutPolling(t *testing.T) {
	const (
		loop  = email.DraftReviewLoopName
		delay = 300 * time.Millisecond
	)
	zero := 0
	cfg := emailPassConfig(emailPassAccount("personal", config.EmailMailboxConfig{
		Owner: config.EmailMailboxOwnerOperator, ReviewLoop: loop, ReviewDelay: delay,
	}))
	cfg.Email.PollInterval = &zero
	a := emailRoutesApp(t, cfg)
	queue := emailPassQueue(t)
	enqueueReviewItem(t, queue, loop, "draft:d-1")
	bus, snapshot := captureBus()
	a.emailReviewWake = newTestReviewWaker(t, queue, bus, cfg.Email.Accounts, nil)
	a.emailReviewWake.recheckFloor = 0

	if err := a.startEmailReviewPasses(t.Context()); err != nil {
		t.Fatalf("startEmailReviewPasses() = %v", err)
	}
	if n := len(snapshot()); n != 1 {
		t.Fatalf("boot sweep woke the loop %d times, want 1", n)
	}
	if err := queue.Defer(t.Context(), loop, "draft:d-1"); err != nil {
		t.Fatalf("defer: %v", err)
	}

	envs := waitFor(t, snapshot, 2, 5*time.Second)
	if len(envs) != 2 {
		t.Fatalf("woken %d times: with polling off, the deferred item never woke the loop again", len(envs))
	}
	first, second := wakeEvent(t, envs[0]), wakeEvent(t, envs[1])
	if gap := second.ObservedAt.Sub(first.ObservedAt); gap < delay {
		t.Errorf("the recheck woke the loop %s after the last wake, sooner than review_delay %s", gap, delay)
	}
	if got := second.Metadata["pending"]; got != "1" {
		t.Errorf("recheck wake pending = %s, want 1", got)
	}

	ackReviewItem(t, queue, loop, "draft:d-1")
	time.Sleep(3 * delay)
	if n := len(snapshot()); n != 2 {
		t.Errorf("woken %d times once the queue emptied, want the 2 before it", n)
	}
	if n := armedRechecks(a.emailReviewWake); n != 0 {
		t.Errorf("%d rechecks armed for an empty queue, want none", n)
	}
}

// TestEmailReviewWakeGivesUpOnABlockedQueue pins the bound on a wake
// attempt. While every queue read blocks, the boot sweep and the
// debounced wake each give up at wakeTimeout, return, log that the queue
// was not read, and wake nothing. Once reads succeed again, the retry
// their recheck armed wakes the loop, and no two wakes come sooner than
// review_delay apart.
func TestEmailReviewWakeGivesUpOnABlockedQueue(t *testing.T) {
	const loop = email.DraftReviewLoopName
	queue, db := emailPassQueueDB(t)
	bus, snapshot := captureBus()
	logs := &syncBuffer{}
	w := newTestReviewWaker(t, queue, bus, reviewAccount(150*time.Millisecond, 5*time.Second), slog.New(slog.NewTextHandler(logs, nil)))
	w.wakeTimeout = 100 * time.Millisecond
	w.recheckFloor = 0
	w.arm()
	enqueueReviewItem(t, queue, loop, "draft:d-1") // arms the debounce

	tx, err := db.BeginTx(t.Context(), nil) // holds the store's one connection
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	start := time.Now()
	w.Sweep(t.Context())
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the boot sweep took %s against a blocked queue; want it bounded by wakeTimeout", took)
	}
	if !waitForLog(logs, 5*time.Second, "not read for wake", "reason=boot_sweep") {
		t.Fatalf("the boot sweep logged no give-up:\n%s", logs)
	}
	if !waitForLog(logs, 5*time.Second, "not read for wake", "reason=enqueue") {
		t.Fatalf("the debounced wake never gave up on the blocked queue:\n%s", logs)
	}
	if n := len(snapshot()); n != 0 {
		t.Fatalf("woken %d times while the queue could not be read", n)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	envs := waitFor(t, snapshot, 1, 5*time.Second)
	if len(envs) != 1 {
		t.Fatalf("the timed-out wakes were never retried:\n%s", logs)
	}
	if got := wakeEvent(t, envs[0]).Metadata["pending"]; got != "1" {
		t.Errorf("retried wake pending = %s, want 1", got)
	}
	// The loop never acknowledges d-1, so its recheck wakes it again
	// each review_delay; a retry that announced it twice would wake it
	// again at once.
	time.Sleep(400 * time.Millisecond)
	envs = snapshot()
	for i := 1; i < len(envs); i++ {
		prev, next := wakeEvent(t, envs[i-1]).ObservedAt, wakeEvent(t, envs[i]).ObservedAt
		if gap := next.Sub(prev); gap < 150*time.Millisecond {
			t.Errorf("wakes %d and %d came %s apart, sooner than review_delay: a retry announced the same work twice", i, i+1, gap)
		}
	}
}

// TestEmailReviewWakerStopCancelsABlockedWake pins shutdown: stop
// returns promptly while a worker is blocked on the queue, cancelling it
// rather than waiting out wakeTimeout, and after stop neither an enqueue
// nor a recheck wakes the loop.
func TestEmailReviewWakerStopCancelsABlockedWake(t *testing.T) {
	const loop = email.DraftReviewLoopName
	queue, db := emailPassQueueDB(t)
	bus, snapshot := captureBus()
	logs := &syncBuffer{}
	w := newTestReviewWaker(t, queue, bus, reviewAccount(100*time.Millisecond, 5*time.Second),
		slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	w.wakeTimeout = time.Minute
	w.recheckFloor = 0
	w.arm()
	enqueueReviewItem(t, queue, loop, "draft:d-1")

	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // the debounce fires at 100ms, and its worker blocks
	start := time.Now()
	w.stop()
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("stop waited %s for a blocked wake; want it cancelled", took)
	}
	if !strings.Contains(logs.String(), "email review wake cancelled") {
		t.Errorf("no blocked wake was cancelled, so the test did not exercise stop:\n%s", logs)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	enqueueReviewItem(t, queue, loop, "draft:d-2")
	time.Sleep(400 * time.Millisecond)
	if n := len(snapshot()); n != 0 {
		t.Errorf("woken %d times after stop", n)
	}
	if n := armedRechecks(w); n != 0 {
		t.Errorf("%d rechecks armed after stop", n)
	}
}

// TestEmailReviewRetriesAFailedReadAtTheFloor pins when a read that
// timed out is retried: once the recheck floor has passed, not a whole
// review_delay later, so a stalled store holds a due wake back by about
// the floor however long the account's review_delay is.
func TestEmailReviewRetriesAFailedReadAtTheFloor(t *testing.T) {
	const loop = email.DraftReviewLoopName
	queue, db := emailPassQueueDB(t)
	enqueueReviewItem(t, queue, loop, "draft:personal:d-1")
	bus, snapshot := captureBus()
	logs := &syncBuffer{}
	w := newTestReviewWaker(t, queue, bus, reviewAccount(time.Hour, 2*time.Hour), slog.New(slog.NewTextHandler(logs, nil)))
	w.wakeTimeout = 100 * time.Millisecond
	w.recheckFloor = 200 * time.Millisecond

	tx, err := db.BeginTx(t.Context(), nil) // holds the store's one connection
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	w.Sweep(t.Context())
	if !strings.Contains(logs.String(), "retry_in=200ms") {
		t.Errorf("the failed read's retry is not logged at the floor:\n%s", logs)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if envs := waitFor(t, snapshot, 1, 5*time.Second); len(envs) != 1 {
		t.Fatalf("a boot sweep that could not read the queue was not retried within seconds; review_delay is an hour:\n%s", logs)
	}
}

// TestEmailReviewRetryKeepsTheLeftoverRecheck pins that a retry never
// strands announced work. A wake arms its recheck for review_delay
// later; an attempt that then cannot read the queue replaces it with a
// sooner retry, and that retry finds nothing due yet. The work the wake
// announced must still wake the loop again, review_delay after the wake.
func TestEmailReviewRetryKeepsTheLeftoverRecheck(t *testing.T) {
	const (
		loop  = email.DraftReviewLoopName
		delay = 1500 * time.Millisecond
	)
	queue, db := emailPassQueueDB(t)
	enqueueReviewItem(t, queue, loop, "draft:personal:d-1")
	bus, snapshot := captureBus()
	logs := &syncBuffer{}
	w := newTestReviewWaker(t, queue, bus, reviewAccount(delay, time.Hour),
		slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	w.wakeTimeout = 100 * time.Millisecond
	w.recheckFloor = 20 * time.Millisecond

	w.Sweep(t.Context()) // wakes for d-1 and arms its recheck at review_delay
	if n := len(snapshot()); n != 1 {
		t.Fatalf("boot sweep woke the loop %d times, want 1", n)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	w.launch(loop, emailReviewReasonEnqueue, w.hasNewWork) // a debounce fire that cannot read the queue
	if !waitForLog(logs, 5*time.Second, "not read for wake", "reason=enqueue") {
		t.Fatalf("the debounced wake never gave up on the blocked queue:\n%s", logs)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !waitForLog(logs, 5*time.Second, "wake not due", "reason=retry") {
		t.Fatalf("no retry found nothing due before the recheck, so the test did not exercise the replacement:\n%s", logs)
	}

	envs := waitFor(t, snapshot, 2, 5*time.Second)
	if len(envs) != 2 {
		t.Fatalf("woken %d times: the retry replaced the recheck and stranded the announced draft:\n%s", len(envs), logs)
	}
	if gap := wakeEvent(t, envs[1]).ObservedAt.Sub(wakeEvent(t, envs[0]).ObservedAt); gap < delay {
		t.Errorf("the recheck woke the loop %s after the last wake, sooner than review_delay %s", gap, delay)
	}
}

// TestEmailReviewWakeLoadsNoItem pins that deciding and sending a wake,
// at the boot sweep or at a recheck, loads none of a large backlog, and
// counts drafts by subject: the payloads here name no type. A wake that
// loaded the partition would allocate at least the backlog's size on
// every debounce fire and every recheck.
func TestEmailReviewWakeLoadsNoItem(t *testing.T) {
	const (
		loop         = email.DraftReviewLoopName
		backlog      = 32
		summaryBytes = 128 << 10 // a 4 MiB backlog
		bound        = 1 << 20
	)
	queue := emailPassQueue(t)
	summary := strings.Repeat("x", summaryBytes)
	for i := range backlog {
		raw, err := json.Marshal(messages.LoopNotifyPayload{Events: []messages.LoopEventPayload{{
			Source:   "email_review",
			Summary:  summary,
			Metadata: map[string]string{"account": "personal"},
		}}})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := queue.Enqueue(t.Context(), loop, "draft:personal:d-"+strconv.Itoa(i), 0, raw); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	bus, snapshot := captureBus()
	w := newTestReviewWaker(t, queue, bus, reviewAccount(15*time.Minute, time.Hour), nil)

	if n := heapAllocated(func() { w.Sweep(t.Context()) }); n > bound {
		t.Errorf("a boot sweep over a 4 MiB backlog allocated %d bytes: the wake loaded the queue", n)
	}
	w.now = func() time.Time { return time.Now().Add(time.Hour) }
	if n := heapAllocated(func() { recheckAll(t, w) }); n > bound {
		t.Errorf("a recheck over a 4 MiB backlog allocated %d bytes: the wake loaded the queue", n)
	}
	envs := snapshot()
	if len(envs) != 2 {
		t.Fatalf("wakes = %d, want the sweep's and the recheck's", len(envs))
	}
	for _, env := range envs {
		if got := wakeEvent(t, env).Metadata["drafts"]; got != strconv.Itoa(backlog) {
			t.Errorf("wake drafts = %s, want %d", got, backlog)
		}
	}
}
