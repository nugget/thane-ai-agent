package loop

import (
	"context"
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
		if req.Hints["supervisor"] != "true" {
			t.Fatalf("supervisor hint = %q, want true", req.Hints["supervisor"])
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
