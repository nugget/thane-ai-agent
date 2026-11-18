package loop

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

func TestPlanMailboxDelivery(t *testing.T) {
	t.Parallel()
	item := func(id string, prior int) MailboxItem { return MailboxItem{ID: id, PriorDeliveries: prior} }
	ids := func(items []MailboxItem) []string { return mailboxItemIDs(items) }
	tests := []struct {
		name          string
		pending       []MailboxItem
		wantBatch     []string
		wantIsolated  bool
		wantExhausted []string
	}{
		{"empty", nil, []string{}, false, nil},
		{"all fresh", []MailboxItem{item("a", 0), item("b", 0)}, []string{"a", "b"}, false, nil},
		{"suspect head goes alone", []MailboxItem{item("p", 1), item("a", 0)}, []string{"p"}, true, nil},
		{"fresh head stops before suspect", []MailboxItem{item("a", 0), item("p", 2), item("b", 0)}, []string{"a"}, false, nil},
		{"exhausted head set aside, next suspect alone", []MailboxItem{item("p", 3), item("a", 1), item("b", 0)}, []string{"a"}, true, []string{"p"}},
		{"exhausted head set aside, fresh run delivered", []MailboxItem{item("p", 3), item("a", 0), item("b", 0)}, []string{"a", "b"}, false, []string{"p"}},
		{"only exhausted", []MailboxItem{item("p", 3), item("q", 4)}, []string{}, false, []string{"p", "q"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batch, isolated, exhausted := planMailboxDelivery(tt.pending, maxMailboxDeliveries)
			if got := ids(batch); !slices.Equal(got, tt.wantBatch) {
				t.Errorf("batch = %v, want %v", got, tt.wantBatch)
			}
			if isolated != tt.wantIsolated {
				t.Errorf("isolated = %v, want %v", isolated, tt.wantIsolated)
			}
			if got := ids(exhausted); len(got) != len(tt.wantExhausted) || (len(got) > 0 && !slices.Equal(got, tt.wantExhausted)) {
				t.Errorf("exhausted = %v, want %v", got, tt.wantExhausted)
			}
		})
	}
}

// payloads returns each delivered item's payload text, in order.
func payloads(items []MailboxItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = string(it.Payload)
	}
	return out
}

func storeOf(mb *Mailbox) *loopqueue.Store { return mb.store }

func TestPoisonItemSetAsideAndConversationProceeds(t *testing.T) {
	t.Parallel()
	mailbox := newTestMailbox(t)
	var (
		mu         sync.Mutex
		deliveries [][]string
	)
	runner := &turnCallbackRunner{fn: func(context.Context, RunRequest, StreamCallback) (*RunResponse, error) {
		return &RunResponse{Content: "ok"}, nil
	}}
	l, err := New(Config{
		Name:         "poison",
		Operation:    OperationEventDriven,
		SleepDefault: 10 * time.Millisecond,
		SleepMax:     50 * time.Millisecond,
		TurnBuilder: func(_ context.Context, input TurnInput) (*AgentTurn, error) {
			got := payloads(input.MailboxItems)
			mu.Lock()
			deliveries = append(deliveries, got)
			mu.Unlock()
			if slices.Contains(got, "P") {
				return nil, errors.New("signal turn preparation panicked: boom")
			}
			return &AgentTurn{Request: Request{Messages: []Message{{Role: "user", Content: "x"}}}}, nil
		},
	}, Deps{Runner: runner, Mailbox: mailbox})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, p := range []string{"P", "I1", "I2"} {
		if _, err := mailbox.enqueue(context.Background(), l.Name(), "test", []byte(p)); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for {
		depth, _ := l.MailboxDepth(context.Background())
		if depth == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("depth = %d; poison item wedged the partition", depth)
		}
		time.Sleep(10 * time.Millisecond)
	}
	l.Stop()
	mu.Lock()
	defer mu.Unlock()
	var withP [][]string
	for _, d := range deliveries {
		if slices.Contains(d, "P") {
			withP = append(withP, d)
		}
	}
	want := [][]string{{"P", "I1", "I2"}, {"P"}, {"P"}}
	if len(withP) != len(want) {
		t.Fatalf("deliveries containing P = %v, want %v", withP, want)
	}
	for i := range want {
		if !slices.Equal(withP[i], want[i]) {
			t.Fatalf("delivery %d of P = %v, want %v (all: %v)", i, withP[i], want[i], deliveries)
		}
	}
	aside, err := storeOf(mailbox).CountSetAside(context.Background(), time.Now().Add(-time.Hour))
	if err != nil || len(aside) != 1 || aside[0].Count != 1 || aside[0].Consumer != "poison" {
		t.Fatalf("CountSetAside = %+v, %v; want one item in poison", aside, err)
	}
}

func TestRunFailureRefundsDelivery(t *testing.T) {
	t.Parallel()
	mailbox := newTestMailbox(t)
	var mu sync.Mutex
	runs := 0
	var sizes []int
	runner := &turnCallbackRunner{fn: func(context.Context, RunRequest, StreamCallback) (*RunResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		runs++
		if runs <= 5 {
			return nil, errors.New("API error 503: overloaded")
		}
		return &RunResponse{Content: "ok"}, nil
	}}
	l, err := New(Config{
		Name:         "outage",
		Operation:    OperationEventDriven,
		SleepDefault: 5 * time.Millisecond,
		SleepMax:     10 * time.Millisecond,
		TurnBuilder: func(_ context.Context, input TurnInput) (*AgentTurn, error) {
			mu.Lock()
			sizes = append(sizes, len(input.MailboxItems))
			mu.Unlock()
			return &AgentTurn{Request: Request{Messages: []Message{{Role: "user", Content: "x"}}}}, nil
		},
	}, Deps{Runner: runner, Mailbox: mailbox})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, p := range []string{"a", "b"} {
		if _, err := mailbox.enqueue(context.Background(), l.Name(), "test", []byte(p)); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for {
		depth, _ := l.MailboxDepth(context.Background())
		if depth == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("depth = %d after outage cleared", depth)
		}
		time.Sleep(5 * time.Millisecond)
	}
	l.Stop()
	mu.Lock()
	defer mu.Unlock()
	for i, n := range sizes {
		if n != 2 {
			t.Fatalf("turn %d delivered %d items; a refunded outage must not isolate (sizes %v)", i, n, sizes)
		}
	}
	if aside, _ := storeOf(mailbox).CountSetAside(context.Background(), time.Time{}); len(aside) != 0 {
		t.Fatalf("outage set items aside: %+v", aside)
	}
}

func TestStopDuringRunRefundsDelivery(t *testing.T) {
	t.Parallel()
	mailbox := newTestMailbox(t)
	started := make(chan struct{})
	runner := &turnCallbackRunner{fn: func(ctx context.Context, _ RunRequest, _ StreamCallback) (*RunResponse, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	l, err := New(Config{
		Name:      "stopping",
		Operation: OperationEventDriven,
		TurnBuilder: func(context.Context, TurnInput) (*AgentTurn, error) {
			return &AgentTurn{Request: Request{Messages: []Message{{Role: "user", Content: "x"}}}}, nil
		},
	}, Deps{Runner: runner, Mailbox: mailbox})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := mailbox.enqueue(context.Background(), l.Name(), "test", []byte("a")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started
	l.Stop()
	items, err := mailbox.Peek(context.Background(), l.Name(), 0)
	if err != nil || len(items) != 1 || items[0].PriorDeliveries != 0 {
		t.Fatalf("after stop: items %+v err %v; want one pending item with 0 prior deliveries", items, err)
	}
}

func TestIsolatedRedeliveryHasNoPullInput(t *testing.T) {
	t.Parallel()
	mailbox := newTestMailbox(t)
	pull := make(chan bool, 4)
	runner := &turnCallbackRunner{fn: func(_ context.Context, req RunRequest, _ StreamCallback) (*RunResponse, error) {
		pull <- req.PullInput != nil
		return &RunResponse{Content: "ok"}, nil
	}}
	l, err := New(Config{
		Name:      "isolated",
		Operation: OperationEventDriven,
		TurnBuilder: func(context.Context, TurnInput) (*AgentTurn, error) {
			return &AgentTurn{
				Request:    Request{Messages: []Message{{Role: "user", Content: "x"}}},
				PullRender: func(context.Context, []MailboxItem) []llm.Message { return nil },
			}, nil
		},
		MaxIter: 1,
	}, Deps{Runner: runner, Mailbox: mailbox})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	it, err := mailbox.enqueue(context.Background(), l.Name(), "test", []byte("p"))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// One earlier delivery that never returned (the process died).
	if err := mailbox.recordDelivery(context.Background(), l.Name(), []MailboxItem{it}); err != nil {
		t.Fatalf("recordDelivery: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case got := <-pull:
		if got {
			t.Fatal("isolated redelivery was given PullInput")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no turn ran")
	}
	<-l.Done()
}

// TestCrashMidTurnCountsDelivery ends the loop goroutine inside the turn
// builder, which is what a process death leaves behind: no settle, no
// ack. Each "boot" is a fresh Loop over the same store.
func TestCrashMidTurnCountsDelivery(t *testing.T) {
	t.Parallel()
	mailbox := newTestMailbox(t)
	for _, p := range []string{"P", "a"} {
		if _, err := mailbox.enqueue(context.Background(), "crashy", "test", []byte(p)); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	boot := func() []string {
		got := make(chan []string, 1)
		l, err := New(Config{
			Name:      "crashy",
			Operation: OperationEventDriven,
			TurnBuilder: func(_ context.Context, input TurnInput) (*AgentTurn, error) {
				ids := payloads(input.MailboxItems)
				got <- ids
				if slices.Contains(ids, "P") {
					runtime.Goexit() // the process dies on P
				}
				return &AgentTurn{Request: Request{Messages: []Message{{Role: "user", Content: "x"}}}}, nil
			},
			MaxIter: 1,
		}, Deps{Runner: &turnCallbackRunner{fn: func(context.Context, RunRequest, StreamCallback) (*RunResponse, error) {
			return &RunResponse{Content: "ok"}, nil
		}}, Mailbox: mailbox})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := l.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		select {
		case ids := <-got:
			<-l.Done()
			return ids
		case <-time.After(2 * time.Second):
			t.Fatal("boot delivered nothing")
			return nil
		}
	}
	want := []string{"P,a", "P", "P", "a"} // boot 4 sets P aside and delivers a alone
	for i, w := range want {
		if got := strings.Join(boot(), ","); got != w {
			t.Fatalf("boot %d delivered %q, want %q", i+1, got, w)
		}
	}
	if depth, _ := mailbox.depth(context.Background(), "crashy"); depth != 0 {
		t.Fatalf("depth = %d after boot 4, want 0", depth)
	}
}

func TestAckLandsWhenLoopStopsAfterCleanTurn(t *testing.T) {
	t.Parallel()
	mailbox := newTestMailbox(t)
	started := make(chan struct{})
	runner := &turnCallbackRunner{fn: func(ctx context.Context, _ RunRequest, _ StreamCallback) (*RunResponse, error) {
		close(started)
		<-ctx.Done()
		return &RunResponse{Content: "sent before the stop landed"}, nil
	}}
	l, err := New(Config{
		Name:      "late-ack",
		Operation: OperationEventDriven,
		TurnBuilder: func(context.Context, TurnInput) (*AgentTurn, error) {
			return &AgentTurn{Request: Request{Messages: []Message{{Role: "user", Content: "x"}}}}, nil
		},
	}, Deps{Runner: runner, Mailbox: mailbox})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := mailbox.enqueue(context.Background(), l.Name(), "test", []byte("a")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started
	l.Stop()
	if depth, _ := mailbox.depth(context.Background(), l.Name()); depth != 0 {
		t.Fatalf("depth = %d; a clean turn's ack was lost to the stop", depth)
	}
}

func TestHandlerFailureRefundsDelivery(t *testing.T) {
	t.Parallel()
	mailbox := newTestMailbox(t)
	l, err := New(Config{
		Name:      "handler",
		Operation: OperationEventDriven,
		Handler:   func(context.Context, any) error { return errors.New("provider down") },
		MaxIter:   1,
	}, Deps{Mailbox: mailbox})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := mailbox.enqueue(context.Background(), l.Name(), "test", []byte("a")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-l.Done()
	items, _ := mailbox.Peek(context.Background(), l.Name(), 0)
	if len(items) != 1 || items[0].PriorDeliveries != 0 {
		t.Fatalf("items = %+v; a Handler failure must not count", items)
	}
}

// TestStopDuringBuildRefundsDelivery stops the loop while the turn
// builder is running, so the builder fails because of the stop: the
// delivery is given back rather than counted against the item.
func TestStopDuringBuildRefundsDelivery(t *testing.T) {
	t.Parallel()
	mailbox := newTestMailbox(t)
	started := make(chan struct{})
	l, err := New(Config{
		Name:      "stopping-build",
		Operation: OperationEventDriven,
		TurnBuilder: func(ctx context.Context, _ TurnInput) (*AgentTurn, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}, Deps{Runner: &noopRunner{}, Mailbox: mailbox})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := mailbox.enqueue(context.Background(), l.Name(), "test", []byte("a")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started
	l.Stop()
	items, err := mailbox.Peek(context.Background(), l.Name(), 0)
	if err != nil || len(items) != 1 || items[0].PriorDeliveries != 0 {
		t.Fatalf("after stop: items %+v err %v; want one pending item with 0 prior deliveries", items, err)
	}
}

func TestMailboxSettleReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		ctxErr        error
		handler       bool
		builderFailed bool
		want          string
	}{
		{"builder failed", nil, false, true, "builder_failed"},
		{"failed after the turn was built", nil, false, false, "failed_after_turn_built"},
		{"handler failed", nil, true, false, "handler_failed"},
		{"stopping while building", context.Canceled, false, true, "loop_stopping"},
		{"stopping in a handler", context.Canceled, true, false, "loop_stopping"},
	}
	for _, tt := range tests {
		if got := mailboxSettleReason(tt.ctxErr, tt.handler, tt.builderFailed); got != tt.want {
			t.Errorf("%s: mailboxSettleReason = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestMailboxAckContinuesPastAFailure acks a batch whose first item
// cannot be acked: the rest of the batch must still be acked.
func TestMailboxAckContinuesPastAFailure(t *testing.T) {
	t.Parallel()
	mailbox := newTestMailbox(t)
	ctx := context.Background()
	first, err := mailbox.enqueue(ctx, "c", "test", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := mailbox.enqueue(ctx, "c", "test", []byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	first.Receipt = "not-this-delivery"
	if err := mailbox.ack(ctx, "c", []MailboxItem{first, second}); err == nil {
		t.Fatal("ack of a superseded item reported no error")
	}
	if depth, _ := mailbox.depth(ctx, "c"); depth != 1 {
		t.Fatalf("depth = %d after the batch ack, want 1 (only the superseded item left)", depth)
	}
}

// TestSettlingAnUncountedDeliveryChangesNothing checks that a delivery
// whose count was never recorded is not refunded, which would take back
// an earlier delivery's count, and that a counted one is.
func TestSettlingAnUncountedDeliveryChangesNothing(t *testing.T) {
	t.Parallel()
	for _, counted := range []bool{false, true} {
		mailbox := newTestMailbox(t)
		ctx := context.Background()
		l := mustNew(t, Config{Name: "settle", Task: "t"}, Deps{Runner: &noopRunner{}, Mailbox: mailbox})
		item, err := mailbox.enqueue(ctx, l.Name(), "test", []byte("a"))
		if err != nil {
			t.Fatal(err)
		}
		if err := mailbox.recordDelivery(ctx, l.Name(), []MailboxItem{item}); err != nil {
			t.Fatal(err)
		}
		pending, _ := mailbox.Peek(ctx, l.Name(), 0)
		delivery := mailboxDelivery{items: pending, counted: counted}
		l.settleFailedMailboxDelivery(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), delivery, errors.New("provider down"), false)
		after, _ := mailbox.Peek(ctx, l.Name(), 0)
		want := 1
		if counted {
			want = 0
		}
		if len(after) != 1 || after[0].PriorDeliveries != want {
			t.Fatalf("counted=%v: prior deliveries after settling = %+v, want %d", counted, after, want)
		}
	}
}

// TestRefundAfterFailedAck checks that a clean turn whose ack failed
// gives back its counted delivery, so its items return as one batch.
func TestRefundAfterFailedAck(t *testing.T) {
	t.Parallel()
	mailbox := newTestMailbox(t)
	ctx := context.Background()
	l := mustNew(t, Config{Name: "ackfail", Task: "t"}, Deps{Runner: &noopRunner{}, Mailbox: mailbox})
	for _, p := range []string{"a", "b"} {
		if _, err := mailbox.enqueue(ctx, l.Name(), "test", []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	pending, _ := mailbox.Peek(ctx, l.Name(), 0)
	if err := mailbox.recordDelivery(ctx, l.Name(), pending); err != nil {
		t.Fatal(err)
	}
	l.refundAfterFailedAck(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), mailboxDelivery{items: pending, counted: true})
	after, _ := mailbox.Peek(ctx, l.Name(), 0)
	if len(after) != 2 || after[0].PriorDeliveries != 0 || after[1].PriorDeliveries != 0 {
		t.Fatalf("after refund: %+v, want both items with 0 prior deliveries", after)
	}
	batch, isolated, _ := planMailboxDelivery(after, maxMailboxDeliveries)
	if len(batch) != 2 || isolated {
		t.Fatalf("next batch = %v isolated=%v, want both items together", payloads(batch), isolated)
	}
}

// TestSetAsideClearsTheErrorStreak checks that setting aside the item
// behind a loop's errors ends the streak, so the loop does not keep
// reporting errors with nothing left to run.
func TestSetAsideClearsTheErrorStreak(t *testing.T) {
	t.Parallel()
	mailbox := newTestMailbox(t)
	l, err := New(Config{
		Name:         "streak",
		Operation:    OperationEventDriven,
		SleepDefault: 10 * time.Millisecond,
		SleepMax:     50 * time.Millisecond,
		TurnBuilder: func(_ context.Context, input TurnInput) (*AgentTurn, error) {
			if len(input.MailboxItems) == 0 {
				return nil, nil // nothing to render, as the Signal builder does
			}
			return nil, errors.New("signal turn preparation panicked: boom")
		},
	}, Deps{Runner: &noopRunner{}, Mailbox: mailbox})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := mailbox.enqueue(context.Background(), l.Name(), "test", []byte("P")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for {
		aside, _ := storeOf(mailbox).CountSetAside(context.Background(), time.Now().Add(-time.Hour))
		st := l.Status()
		if len(aside) == 1 && st.ConsecutiveErrors == 0 && st.LastError == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("set aside %+v, consecutive errors %d, last error %q; want the item set aside and the streak cleared", aside, st.ConsecutiveErrors, st.LastError)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
