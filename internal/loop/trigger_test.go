package loop

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTriggerRunQueuesOverridesAndWake(t *testing.T) {
	l := &Loop{
		id:            "loop-1",
		config:        Config{Name: "battery_monitor"},
		state:         StateSleeping,
		triggerWakeCh: make(chan struct{}, 1),
	}

	result, err := l.TriggerRun(TriggerOptions{
		ForceSupervisor: true,
		ContextMessage:  "Battery cluster looks unstable.",
	})
	if err != nil {
		t.Fatalf("TriggerRun: %v", err)
	}
	if result.LoopID != "loop-1" || result.Name != "battery_monitor" {
		t.Fatalf("result identity = %+v, want loop-1/battery_monitor", result)
	}
	if !result.ForceSupervisor || !result.ContextInjected {
		t.Fatalf("result = %+v, want forced supervisor with injected context", result)
	}

	select {
	case <-l.triggerWakeCh:
	default:
		t.Fatal("expected wake signal to be queued")
	}

	forceSupervisor, messages := l.consumeTriggerOverrides()
	if !forceSupervisor {
		t.Fatal("expected forceSupervisor override to be consumed")
	}
	if len(messages) != 1 || messages[0] != "Battery cluster looks unstable." {
		t.Fatalf("messages = %v, want single trigger context", messages)
	}

	forceSupervisor, messages = l.consumeTriggerOverrides()
	if forceSupervisor || len(messages) != 0 {
		t.Fatalf("second consume = (%v, %v), want empty overrides", forceSupervisor, messages)
	}
}

func TestTriggerRunRejectsEventDrivenOrAwakeLoops(t *testing.T) {
	sleepingEventLoop := &Loop{
		config: Config{
			Name:     "event_loop",
			WaitFunc: func(context.Context) (any, error) { return nil, nil },
		},
		state:         StateSleeping,
		triggerWakeCh: make(chan struct{}, 1),
	}
	if _, err := sleepingEventLoop.TriggerRun(TriggerOptions{}); err == nil || !strings.Contains(err.Error(), "event-driven") {
		t.Fatalf("event-driven TriggerRun error = %v, want event-driven rejection", err)
	}

	awakeLoop := &Loop{
		config:        Config{Name: "awake_loop"},
		state:         StateProcessing,
		triggerWakeCh: make(chan struct{}, 1),
	}
	if _, err := awakeLoop.TriggerRun(TriggerOptions{}); err == nil || !strings.Contains(err.Error(), "not sleeping") {
		t.Fatalf("awake TriggerRun error = %v, want not sleeping rejection", err)
	}
}

func TestTriggerRunWakesSleepingLoopAndInjectsContext(t *testing.T) {
	reqCh := make(chan Request, 1)
	runner := &callbackRunner{
		fn: func(_ context.Context, req RunRequest) (*RunResponse, error) {
			reqCh <- req
			return &RunResponse{
				Content:      "ok",
				Model:        "test-model",
				FinishReason: "stop",
				InputTokens:  10,
				OutputTokens: 5,
			}, nil
		},
	}

	l, err := New(Config{
		Name:         "wake_test",
		Task:         "Inspect the batteries.",
		SleepMin:     time.Hour,
		SleepMax:     time.Hour,
		SleepDefault: time.Hour,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
	}, Deps{Runner: runner, Rand: fixedRand{0}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		l.Stop()
		if done := l.Done(); done != nil {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("loop did not stop")
			}
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.Status().State == StateSleeping {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if l.Status().State != StateSleeping {
		t.Fatalf("loop state = %s, want sleeping before trigger", l.Status().State)
	}

	result, err := l.TriggerRun(TriggerOptions{
		ForceSupervisor: true,
		ContextMessage:  "One-shot trigger: battery voltage anomaly.",
	})
	if err != nil {
		t.Fatalf("TriggerRun: %v", err)
	}
	if !result.ForceSupervisor || !result.ContextInjected {
		t.Fatalf("result = %+v, want forced supervisor with context", result)
	}

	select {
	case req := <-reqCh:
		if got := req.Messages[0].Content; !strings.Contains(got, "Triggered context:\nOne-shot trigger: battery voltage anomaly.") {
			t.Fatalf("request content = %q, want injected trigger context", got)
		}
		if req.Hints["supervisor"] != "true" {
			t.Fatalf("supervisor hint = %q, want true", req.Hints["supervisor"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for triggered run")
	}
}
