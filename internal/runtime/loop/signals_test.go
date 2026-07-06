package loop

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

func newTestMailbox(t *testing.T) *Mailbox {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatalf("new loop queue: %v", err)
	}
	return NewMailbox(store)
}

func TestLoopNotifyWakesSleepingLoopAndPrependsSignalContext(t *testing.T) {
	t.Parallel()

	reqs := make(chan RunRequest, 1)
	runner := &inspectingRunner{
		onRun: func(req RunRequest) {
			reqs <- req
		},
	}
	l, err := New(Config{
		Name:         "signal-test",
		Task:         "Maintain a current view.",
		SleepMin:     time.Hour,
		SleepMax:     time.Hour,
		SleepDefault: time.Hour,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
		Supervisor:   true,
	}, Deps{Runner: runner, Rand: fixedRand{0}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitForLoopState(t, l, StateSleeping)

	env, err := (messages.Envelope{
		From: messages.Identity{Kind: messages.IdentityCore, Name: "core"},
		To: messages.Destination{
			Kind:     messages.DestinationLoop,
			Target:   l.Name(),
			Selector: messages.SelectorName,
		},
		Type: messages.TypeSignal,
		Payload: messages.LoopNotifyPayload{
			Message:         "The garage reading is CPU temperature, not ambient.",
			ForceSupervisor: true,
		},
	}).Normalize(time.Now())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	receipt, err := l.enqueueNotify(env)
	if err != nil {
		t.Fatalf("enqueueNotify: %v", err)
	}
	if !receipt.WokeImmediately {
		t.Fatalf("receipt = %#v, want woke_immediately", receipt)
	}

	select {
	case req := <-reqs:
		if req.RoutingFactors["supervisor"] != "true" {
			t.Fatalf("supervisor hint = %q, want true", req.RoutingFactors["supervisor"])
		}
		content := req.Messages[0].Content
		if !strings.Contains(content, "Loop notifications for this run:") {
			t.Fatalf("task content missing signal prefix: %q", content)
		}
		if !strings.Contains(content, "garage reading is CPU temperature") {
			t.Fatalf("task content missing signal message: %q", content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not wake after signal")
	}
	l.Stop()

	select {
	case <-l.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not finish")
	}
}

func TestLoopNotifyWakesEventDrivenLoop(t *testing.T) {
	t.Parallel()

	inputs := make(chan TurnInput, 1)
	l, err := New(Config{
		Name: "event-driven",
		WaitFunc: func(ctx context.Context) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		TurnBuilder: func(_ context.Context, input TurnInput) (*AgentTurn, error) {
			inputs <- input
			return &AgentTurn{
				Request: Request{
					Messages: []Message{{Role: "user", Content: "wake from notification"}},
				},
			}, nil
		},
		MaxIter:    1,
		Supervisor: true,
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	waitForLoopState(t, l, StateWaiting)

	env, err := (messages.Envelope{
		From: messages.Identity{Kind: messages.IdentityCore},
		To: messages.Destination{
			Kind:     messages.DestinationLoop,
			Target:   l.Name(),
			Selector: messages.SelectorName,
		},
		Type: messages.TypeSignal,
		Payload: messages.LoopNotifyPayload{
			Concern:         "The watcher saw a pattern that may need the owner.",
			ForceSupervisor: true,
		},
	}).Normalize(time.Now())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	receipt, err := l.enqueueNotify(env)
	if err != nil {
		t.Fatalf("enqueueNotify: %v", err)
	}
	if !receipt.WokeImmediately {
		t.Fatalf("receipt = %#v, want woke_immediately", receipt)
	}

	select {
	case input := <-inputs:
		if _, ok := input.Event.(notifyWakeEvent); !ok {
			t.Fatalf("event = %T, want notifyWakeEvent", input.Event)
		}
		if !input.Supervisor {
			t.Fatal("input.Supervisor = false, want forced supervisor")
		}
		if len(input.NotifyEnvelopes) != 1 {
			t.Fatalf("NotifyEnvelopes len = %d, want 1", len(input.NotifyEnvelopes))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event-driven loop did not wake after signal")
	}
}

func TestLoopMailboxWakesEventDrivenLoopAndDrainsAll(t *testing.T) {
	t.Parallel()

	inputs := make(chan TurnInput, 1)
	mailbox := newTestMailbox(t)
	l, err := New(Config{
		Name:      "mailbox-driven",
		Operation: OperationEventDriven,
		TurnBuilder: func(_ context.Context, input TurnInput) (*AgentTurn, error) {
			inputs <- input
			return &AgentTurn{
				Request: Request{
					Messages: []Message{{Role: "user", Content: "mailbox wake"}},
				},
			}, nil
		},
		MaxIter: 1,
	}, Deps{Runner: &noopRunner{}, Mailbox: mailbox})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := mailbox.enqueue(context.Background(), l.Name(), "test", []byte(`{"message":"one"}`)); err != nil {
		t.Fatalf("enqueue mailbox one: %v", err)
	}
	if _, err := mailbox.enqueue(context.Background(), l.Name(), "test", []byte(`{"message":"two"}`)); err != nil {
		t.Fatalf("enqueue mailbox two: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	select {
	case input := <-inputs:
		if len(input.MailboxItems) != 2 {
			t.Fatalf("MailboxItems len = %d, want 2", len(input.MailboxItems))
		}
		if got := string(input.MailboxItems[0].Payload); got != `{"message":"one"}` {
			t.Fatalf("first payload = %q", got)
		}
		if got := string(input.MailboxItems[1].Payload); got != `{"message":"two"}` {
			t.Fatalf("second payload = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event-driven loop did not wake after mailbox enqueue")
	}
	select {
	case <-l.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not finish")
	}

	depth, err := l.MailboxDepth(context.Background())
	if err != nil {
		t.Fatalf("mailbox depth: %v", err)
	}
	if depth != 0 {
		t.Fatalf("mailbox depth = %d, want 0 after turn drain", depth)
	}
}

func TestLoopMailboxKeepsItemsWhenTurnFails(t *testing.T) {
	t.Parallel()

	inputs := make(chan TurnInput, 1)
	mailbox := newTestMailbox(t)
	runner := &turnCallbackRunner{fn: func(context.Context, RunRequest, StreamCallback) (*RunResponse, error) {
		return nil, errors.New("runner unavailable")
	}}
	l, err := New(Config{
		Name:      "mailbox-failure",
		Operation: OperationEventDriven,
		TurnBuilder: func(_ context.Context, input TurnInput) (*AgentTurn, error) {
			inputs <- input
			return &AgentTurn{
				Request: Request{
					Messages: []Message{{Role: "user", Content: "mailbox wake"}},
				},
			}, nil
		},
		MaxIter: 1,
	}, Deps{Runner: runner, Mailbox: mailbox})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := mailbox.enqueue(context.Background(), l.Name(), "test", []byte(`{"message":"one"}`)); err != nil {
		t.Fatalf("enqueue mailbox: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case input := <-inputs:
		if len(input.MailboxItems) != 1 {
			t.Fatalf("MailboxItems len = %d, want 1", len(input.MailboxItems))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event-driven loop did not wake after mailbox enqueue")
	}
	select {
	case <-l.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not finish")
	}

	depth, err := l.MailboxDepth(context.Background())
	if err != nil {
		t.Fatalf("mailbox depth: %v", err)
	}
	if depth != 1 {
		t.Fatalf("mailbox depth = %d, want 1 after failed turn", depth)
	}
}

func TestLoopMailboxRetriesAfterFailedTurn(t *testing.T) {
	t.Parallel()

	inputs := make(chan TurnInput, 2)
	mailbox := newTestMailbox(t)
	runs := 0
	runner := &turnCallbackRunner{fn: func(context.Context, RunRequest, StreamCallback) (*RunResponse, error) {
		runs++
		if runs == 1 {
			return nil, errors.New("transient runner failure")
		}
		return &RunResponse{Content: "ok"}, nil
	}}
	l, err := New(Config{
		Name:         "mailbox-retry",
		Operation:    OperationEventDriven,
		SleepDefault: 10 * time.Millisecond,
		SleepMax:     50 * time.Millisecond,
		TurnBuilder: func(_ context.Context, input TurnInput) (*AgentTurn, error) {
			inputs <- input
			return &AgentTurn{
				Request: Request{
					Messages: []Message{{Role: "user", Content: "mailbox wake"}},
				},
			}, nil
		},
		MaxIter: 2,
	}, Deps{Runner: runner, Mailbox: mailbox})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := mailbox.enqueue(context.Background(), l.Name(), "test", []byte(`{"message":"one"}`)); err != nil {
		t.Fatalf("enqueue mailbox: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		select {
		case input := <-inputs:
			if len(input.MailboxItems) != 1 {
				t.Fatalf("attempt %d MailboxItems len = %d, want 1", attempt, len(input.MailboxItems))
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("attempt %d never started; failed turn was not retried", attempt)
		}
	}
	select {
	case <-l.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not finish")
	}

	depth, err := l.MailboxDepth(context.Background())
	if err != nil {
		t.Fatalf("mailbox depth: %v", err)
	}
	if depth != 0 {
		t.Fatalf("mailbox depth = %d, want 0 after successful retry", depth)
	}
}

func TestLoopMailboxCapsItemsPerWake(t *testing.T) {
	t.Parallel()

	inputs := make(chan TurnInput, 1)
	mailbox := newTestMailbox(t)
	l, err := New(Config{
		Name:      "mailbox-capped",
		Operation: OperationEventDriven,
		TurnBuilder: func(_ context.Context, input TurnInput) (*AgentTurn, error) {
			inputs <- input
			return &AgentTurn{
				Request: Request{
					Messages: []Message{{Role: "user", Content: "mailbox wake"}},
				},
			}, nil
		},
		MaxIter: 1,
	}, Deps{Runner: &noopRunner{}, Mailbox: mailbox})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < maxMailboxItemsPerWake+1; i++ {
		if _, err := mailbox.enqueue(context.Background(), l.Name(), "test", []byte(`{"message":"one"}`)); err != nil {
			t.Fatalf("enqueue mailbox %d: %v", i, err)
		}
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case input := <-inputs:
		if len(input.MailboxItems) != maxMailboxItemsPerWake {
			t.Fatalf("MailboxItems len = %d, want %d", len(input.MailboxItems), maxMailboxItemsPerWake)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event-driven loop did not wake after mailbox enqueue")
	}
	select {
	case <-l.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not finish")
	}

	depth, err := l.MailboxDepth(context.Background())
	if err != nil {
		t.Fatalf("mailbox depth: %v", err)
	}
	if depth != 1 {
		t.Fatalf("mailbox depth = %d, want 1 after capped turn", depth)
	}
}

func TestLoopMailboxIncrementalDrain(t *testing.T) {
	t.Parallel()

	mailbox := newTestMailbox(t)
	l, err := New(Config{
		Name:      "mailbox-incremental",
		Operation: OperationEventDriven,
		Task:      "handle mailbox",
	}, Deps{Runner: &noopRunner{}, Mailbox: mailbox})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := mailbox.enqueue(context.Background(), l.Name(), "test", []byte(`{"message":"one"}`)); err != nil {
		t.Fatalf("enqueue one: %v", err)
	}
	if _, err := mailbox.enqueue(context.Background(), l.Name(), "test", []byte(`{"message":"two"}`)); err != nil {
		t.Fatalf("enqueue two: %v", err)
	}

	items, err := l.DrainMailbox(context.Background(), 1)
	if err != nil {
		t.Fatalf("DrainMailbox: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	depth, err := l.MailboxDepth(context.Background())
	if err != nil {
		t.Fatalf("MailboxDepth: %v", err)
	}
	if depth != 2 {
		t.Fatalf("depth = %d, want 2 after limited peek", depth)
	}
	if err := l.AckMailbox(context.Background(), items); err != nil {
		t.Fatalf("AckMailbox: %v", err)
	}
	depth, err = l.MailboxDepth(context.Background())
	if err != nil {
		t.Fatalf("MailboxDepth after ack: %v", err)
	}
	if depth != 1 {
		t.Fatalf("depth = %d, want 1 after limited ack", depth)
	}
}

func TestWaitForEventCancellationWinsOverNotifyWake(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name: "event-driven-cancel",
		WaitFunc: func(ctx context.Context) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Task: "watch",
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l.wakeCh <- struct{}{}

	event, err := l.waitForEvent(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForEvent err = %v, want context.Canceled", err)
	}
	if event != nil {
		t.Fatalf("event = %#v, want nil", event)
	}
}

func TestFormatNotifyEnvelopesCapsStructuredEvents(t *testing.T) {
	t.Parallel()

	events := make([]messages.LoopEventPayload, messages.MaxLoopEventsPerWake+1)
	for i := range events {
		events[i] = messages.LoopEventPayload{
			Source: "test",
			Type:   "item",
			ID:     "event",
		}
	}
	summary := FormatNotifyEnvelopes([]messages.Envelope{{
		From: messages.Identity{Kind: messages.IdentitySystem, Name: "tester"},
		To: messages.Destination{
			Kind:     messages.DestinationLoop,
			Target:   "curator",
			Selector: messages.SelectorName,
		},
		Type: messages.TypeSignal,
		Payload: messages.LoopNotifyPayload{
			Kind:    "event_source",
			Message: "derived event summary",
			Events:  events,
		},
	}})

	if !strings.Contains(summary, `"events_truncated":true`) {
		t.Fatalf("summary missing truncation flag: %s", summary)
	}
	if !strings.Contains(summary, `"events_total":`+strconv.Itoa(messages.MaxLoopEventsPerWake+1)) {
		t.Fatalf("summary missing total count: %s", summary)
	}
	if !strings.Contains(summary, `"events_shown":`+strconv.Itoa(messages.MaxLoopEventsPerWake)) {
		t.Fatalf("summary missing shown count: %s", summary)
	}
	if strings.Contains(summary, "derived event summary") {
		t.Fatalf("summary should omit derived message when structured events are present: %s", summary)
	}
}

func TestLoopNotifyQueueBounded(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name: "queue-bounded",
		Task: "watch",
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	l.mu.Lock()
	l.started = true
	l.state = StateProcessing
	l.mu.Unlock()

	env, err := (messages.Envelope{
		From: messages.Identity{Kind: messages.IdentityCore},
		To: messages.Destination{
			Kind:     messages.DestinationLoop,
			Target:   l.Name(),
			Selector: messages.SelectorName,
		},
		Type: messages.TypeSignal,
	}).Normalize(time.Now())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	for i := 0; i < maxPendingNotifications; i++ {
		if _, err := l.enqueueNotify(env); err != nil {
			t.Fatalf("enqueueNotify(%d): %v", i, err)
		}
	}
	if _, err := l.enqueueNotify(env); err == nil || !strings.Contains(err.Error(), "queue full") {
		t.Fatalf("enqueueNotify overflow err = %v, want queue-full rejection", err)
	}
}

// TestOperationEventDrivenReportsEventDrivenStatus pins the P2 fix:
// status, logs, and snapshots all key off [Loop.isEventDriven], which
// returns true for both WaitFunc-based loops AND OperationEventDriven
// specs. Pre-fix, a persisted operation: event_driven loop showed up
// as timed/non-event-driven in /api/loops and the dashboard.
func TestOperationEventDrivenReportsEventDrivenStatus(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name:      "declared-event-driven",
		Task:      "watch",
		Operation: OperationEventDriven,
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	status := l.Status()
	if !status.EventDriven {
		t.Errorf("Status.EventDriven = false, want true for OperationEventDriven")
	}
}

// TestLoopNotifyPokesWakeChWhileProcessing pins the P1 fix: a
// notification arriving while an event-driven loop is in StateProcessing
// must still poke wakeCh so the next waitForWake doesn't strand the
// pendingNotify. Pre-fix, enqueueNotify skipped the channel signal when
// state != Sleeping/Waiting, leaving the queued item until some later
// unrelated wake repoked the channel.
func TestLoopNotifyPokesWakeChWhileProcessing(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name: "busy-event-driven",
		Task: "watch",
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	l.mu.Lock()
	l.started = true
	l.state = StateProcessing
	l.mu.Unlock()

	env, err := (messages.Envelope{
		From: messages.Identity{Kind: messages.IdentityCore},
		To: messages.Destination{
			Kind:     messages.DestinationLoop,
			Target:   l.Name(),
			Selector: messages.SelectorName,
		},
		Type: messages.TypeSignal,
	}).Normalize(time.Now())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	receipt, err := l.enqueueNotify(env)
	if err != nil {
		t.Fatalf("enqueueNotify: %v", err)
	}
	if receipt.WokeImmediately {
		t.Errorf("WokeImmediately = true, want false (state was Processing)")
	}
	if !receipt.QueuedForNextWake {
		t.Errorf("QueuedForNextWake = false, want true")
	}
	// The channel must be non-empty so the next waitForWake returns
	// instead of blocking.
	select {
	case <-l.wakeCh:
	default:
		t.Fatal("wakeCh was not signaled while loop in StateProcessing — next waitForWake would block and strand the queued notification")
	}
}

// TestConsumePendingNotifiesDecodesPointerAndMapPayloads exercises the
// P2/Copilot tag-aggregation fix: tags arriving on *LoopNotifyPayload
// and map[string]any payloads now contribute to the iteration's
// returned tag set. Pre-fix, only the concrete LoopNotifyPayload type
// assertion fired, silently dropping the others.
func TestConsumePendingNotifiesDecodesPointerAndMapPayloads(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name: "tag-decode",
		Task: "watch",
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	l.mu.Lock()
	l.started = true
	l.state = StateProcessing
	l.mu.Unlock()

	dest := messages.Destination{Kind: messages.DestinationLoop, Target: l.Name(), Selector: messages.SelectorName}

	pointerEnv, err := (messages.Envelope{
		From: messages.Identity{Kind: messages.IdentityCore}, To: dest, Type: messages.TypeSignal,
		Payload: &messages.LoopNotifyPayload{Tags: []string{"pointer_tag"}},
	}).Normalize(time.Now())
	if err != nil {
		t.Fatalf("Normalize pointer: %v", err)
	}
	mapEnv, err := (messages.Envelope{
		From: messages.Identity{Kind: messages.IdentityCore}, To: dest, Type: messages.TypeSignal,
		Payload: map[string]any{"tags": []any{"map_tag", "pointer_tag"}},
	}).Normalize(time.Now())
	if err != nil {
		t.Fatalf("Normalize map: %v", err)
	}
	for _, env := range []messages.Envelope{pointerEnv, mapEnv} {
		if _, err := l.enqueueNotify(env); err != nil {
			t.Fatalf("enqueueNotify: %v", err)
		}
	}

	_, _, tags := l.consumePendingNotifies()
	got := map[string]bool{}
	for _, tag := range tags {
		got[tag] = true
	}
	if !got["pointer_tag"] || !got["map_tag"] {
		t.Fatalf("tags = %v, want both pointer_tag and map_tag", tags)
	}
	if len(tags) != 2 {
		t.Fatalf("tags = %v, want exactly 2 unique", tags)
	}
}

func TestLoopNotifyTagsFlowIntoInitialTags(t *testing.T) {
	t.Parallel()

	reqs := make(chan RunRequest, 1)
	runner := &inspectingRunner{
		onRun: func(req RunRequest) {
			reqs <- req
		},
	}
	l, err := New(Config{
		Name:         "tag-wake",
		Task:         "Triage incoming notifications.",
		SleepMin:     time.Hour,
		SleepMax:     time.Hour,
		SleepDefault: time.Hour,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
	}, Deps{Runner: runner, Rand: fixedRand{0}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitForLoopState(t, l, StateSleeping)

	env, err := (messages.Envelope{
		From: messages.Identity{Kind: messages.IdentityCore, Name: "core"},
		To: messages.Destination{
			Kind:     messages.DestinationLoop,
			Target:   l.Name(),
			Selector: messages.SelectorName,
		},
		Type: messages.TypeSignal,
		Payload: messages.LoopNotifyPayload{
			Message: "Triage triggered by classifier.",
			Tags:    []string{"owner", "untrusted"},
		},
	}).Normalize(time.Now())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if _, err := l.enqueueNotify(env); err != nil {
		t.Fatalf("enqueueNotify: %v", err)
	}

	select {
	case req := <-reqs:
		got := map[string]bool{}
		for _, tag := range req.InitialTags {
			got[tag] = true
		}
		if !got["owner"] || !got["untrusted"] {
			t.Fatalf("InitialTags = %v, want owner+untrusted", req.InitialTags)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not wake after tag-carrying signal")
	}
	l.Stop()
	select {
	case <-l.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not finish")
	}
}

func TestConsumePendingNotifiesAggregatesWakeTags(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name: "tag-aggregator",
		Task: "watch",
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	l.mu.Lock()
	l.started = true
	l.state = StateProcessing
	l.mu.Unlock()

	mkEnv := func(tags []string) messages.Envelope {
		env, err := (messages.Envelope{
			From: messages.Identity{Kind: messages.IdentityCore},
			To: messages.Destination{
				Kind:     messages.DestinationLoop,
				Target:   l.Name(),
				Selector: messages.SelectorName,
			},
			Type: messages.TypeSignal,
			Payload: messages.LoopNotifyPayload{
				Tags: tags,
			},
		}).Normalize(time.Now())
		if err != nil {
			t.Fatalf("Normalize: %v", err)
		}
		return env
	}

	if _, err := l.enqueueNotify(mkEnv([]string{"owner", "security"})); err != nil {
		t.Fatalf("enqueueNotify: %v", err)
	}
	if _, err := l.enqueueNotify(mkEnv([]string{"owner", "device_control", ""})); err != nil {
		t.Fatalf("enqueueNotify: %v", err)
	}
	if _, err := l.enqueueNotify(mkEnv(nil)); err != nil {
		t.Fatalf("enqueueNotify: %v", err)
	}

	envs, force, tags := l.consumePendingNotifies()
	if len(envs) != 3 {
		t.Fatalf("envelopes len = %d, want 3", len(envs))
	}
	if force {
		t.Fatalf("forceSupervisor = true, want false")
	}
	wantTags := map[string]bool{"owner": true, "security": true, "device_control": true}
	if len(tags) != len(wantTags) {
		t.Fatalf("tags = %v, want %d unique", tags, len(wantTags))
	}
	for _, tag := range tags {
		if !wantTags[tag] {
			t.Fatalf("unexpected tag %q in %v", tag, tags)
		}
	}
}

func TestSummarizeNotifyEnvelopesOrdersUrgentFirst(t *testing.T) {
	t.Parallel()

	mkEnv := func(id string, priority messages.Priority, message string) messages.Envelope {
		return messages.Envelope{
			ID:       id,
			From:     messages.Identity{Kind: messages.IdentitySystem, Name: "tester"},
			Priority: priority,
			Payload: messages.LoopNotifyPayload{
				Kind:    "test",
				Message: message,
			},
		}
	}

	envs := []messages.Envelope{
		mkEnv("low-1", messages.PriorityLow, "low signal"),
		mkEnv("normal-1", messages.PriorityNormal, "normal one"),
		mkEnv("urgent-1", messages.PriorityUrgent, "urgent one"),
		mkEnv("normal-2", messages.PriorityNormal, "normal two"),
		mkEnv("urgent-2", messages.PriorityUrgent, "urgent two"),
	}

	summary := summarizeNotifyEnvelopes(envs)
	idxUrgent1 := strings.Index(summary, `"urgent-1"`)
	idxUrgent2 := strings.Index(summary, `"urgent-2"`)
	idxNormal1 := strings.Index(summary, `"normal-1"`)
	idxNormal2 := strings.Index(summary, `"normal-2"`)
	idxLow := strings.Index(summary, `"low-1"`)
	if idxUrgent1 < 0 || idxUrgent2 < 0 || idxNormal1 < 0 || idxNormal2 < 0 || idxLow < 0 {
		t.Fatalf("missing id in summary: %s", summary)
	}
	// Urgent must precede normal which must precede low.
	if idxUrgent1 >= idxNormal1 || idxUrgent2 >= idxNormal1 {
		t.Fatalf("urgent envelopes not rendered before normal: %s", summary)
	}
	if idxNormal1 >= idxLow || idxNormal2 >= idxLow {
		t.Fatalf("normal envelopes not rendered before low: %s", summary)
	}
	// Within urgent bucket, arrival order is preserved.
	if idxUrgent1 >= idxUrgent2 {
		t.Fatalf("urgent ordering not stable: %s", summary)
	}
	if idxNormal1 >= idxNormal2 {
		t.Fatalf("normal ordering not stable: %s", summary)
	}
}

func waitForLoopState(t *testing.T, l *Loop, want State) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.Status().State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("state = %q, want %q", l.Status().State, want)
}
