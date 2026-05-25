package loop

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

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
