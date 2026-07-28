package loop

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSleepEnvelopeOnlyForLoopsThatPaceThemselves pins the gate. The
// envelope is a promise about a choice the loop gets to make, so it must
// appear on exactly the loops that can make it — the same set
// set_next_sleep accepts. A container or an event-driven loop reporting
// bounds would describe a cadence it does not have.
func TestSleepEnvelopeOnlyForLoopsThatPaceThemselves(t *testing.T) {
	tests := []struct {
		name        string
		operation   string
		eventDriven bool
		want        bool
	}{
		{"timer-driven service loop", string(OperationService), false, true},
		{"event-driven service loop", string(OperationService), true, false},
		{"container", string(OperationContainer), false, false},
		{"event-driven operation", string(OperationEventDriven), false, false},
		{"background task", string(OperationBackgroundTask), false, false},
		{"request/reply", string(OperationRequestReply), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newSleepEnvelope(tt.operation, tt.eventDriven, 15*time.Minute, time.Hour, 30*time.Minute, nil)
			if (got != nil) != tt.want {
				t.Fatalf("envelope present = %v, want %v", got != nil, tt.want)
			}
		})
	}
}

func TestSleepEnvelopeRendersDurationsAndJitter(t *testing.T) {
	jitter := 0.25
	env := newSleepEnvelope(string(OperationService), false, 15*time.Minute, 12*time.Hour, time.Hour, &jitter)
	if env == nil {
		t.Fatal("service loop should carry an envelope")
	}
	if env.Min != "15m" || env.Max != "12h" || env.Default != "1h" {
		t.Errorf("envelope = %#v, want 15m/12h/1h", env)
	}
	if env.Jitter != 0.25 {
		t.Errorf("jitter = %v, want 0.25", env.Jitter)
	}
}

// TestSleepEnvelopeFillsRuntimeDefaults covers the stored-definition
// case: a spec that declared no envelope still runs under one, and
// leaving the model to recover the fallback is exactly the implied-default
// this is meant to remove.
func TestSleepEnvelopeFillsRuntimeDefaults(t *testing.T) {
	env := newSleepEnvelope(string(OperationService), false, 0, 0, 0, nil)
	if env == nil {
		t.Fatal("service loop should carry an envelope")
	}
	if env.Min != "30s" || env.Max != "5m" || env.Default != "1m" {
		t.Errorf("envelope = %#v, want the runtime defaults 30s/5m/1m", env)
	}
	if env.Jitter != DefaultJitter {
		t.Errorf("jitter = %v, want the default %v", env.Jitter, DefaultJitter)
	}
}

// TestSleepEnvelopeZeroJitterIsNotTheDefault distinguishes "jitter not
// declared" from "jitter explicitly off" — the same distinction
// Config.Jitter's pointer exists for.
func TestSleepEnvelopeZeroJitterIsNotTheDefault(t *testing.T) {
	off := 0.0
	env := newSleepEnvelope(string(OperationService), false, time.Minute, time.Hour, 5*time.Minute, &off)
	if env == nil {
		t.Fatal("service loop should carry an envelope")
	}
	if env.Jitter != 0 {
		t.Errorf("jitter = %v, want 0 for an explicitly disabled jitter", env.Jitter)
	}
}

// TestSleepControlToolAdvertisesThisLoopsBounds is the point of #1313:
// the advertisement the model reads while choosing an argument names the
// numbers, instead of referring to bounds it has no way to see.
func TestSleepControlToolAdvertisesThisLoopsBounds(t *testing.T) {
	l, err := New(Config{
		Name:         "reservoir_watch",
		Task:         "Watch the reservoir.",
		Operation:    OperationService,
		SleepMin:     15 * time.Minute,
		SleepMax:     12 * time.Hour,
		SleepDefault: time.Hour,
	}, Deps{Runner: &countingRunner{count: &atomic.Int32{}}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tool, ok := l.sleepControlRuntimeTool()
	if !ok {
		t.Fatal("a timer-driven service loop must get a loop-scoped set_next_sleep")
	}
	if tool.Name != SetNextSleepToolName {
		t.Fatalf("tool name = %q, want %q so it shadows the global registration", tool.Name, SetNextSleepToolName)
	}
	for _, want := range []string{"15m", "12h", "1h", "clamped"} {
		if !strings.Contains(tool.Description, want) {
			t.Errorf("description missing %q\n--- got ---\n%s", want, tool.Description)
		}
	}
	// The parameter description carries the range too: some model
	// families surface argument schemas more prominently than the tool
	// blurb, and the number has to be wherever the model is looking.
	props, _ := tool.Parameters["properties"].(map[string]any)
	duration, _ := props["duration"].(map[string]any)
	if desc, _ := duration["description"].(string); !strings.Contains(desc, "15m") || !strings.Contains(desc, "12h") {
		t.Errorf("duration parameter description does not name the range: %q", desc)
	}
}

func TestSleepControlToolAbsentForLoopsWithNoSleepToChoose(t *testing.T) {
	l, err := New(Config{
		Name:       "dispatch",
		Task:       "Handle one request.",
		Operation:  OperationBackgroundTask,
		Completion: CompletionNone,
	}, Deps{Runner: &countingRunner{count: &atomic.Int32{}}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := l.sleepControlRuntimeTool(); ok {
		t.Error("a background task has no sleep to choose and must not be offered the tool")
	}
}

// TestHandleSetNextSleepClampsAndSaysSo covers the failure the issue was
// filed about: a request outside the envelope is applied at a different
// value, and the result has to say so rather than reading as a plain ok.
func TestHandleSetNextSleepClampsAndSaysSo(t *testing.T) {
	l, err := New(Config{
		Name:         "reservoir_watch",
		Task:         "Watch the reservoir.",
		Operation:    OperationService,
		SleepMin:     15 * time.Minute,
		SleepMax:     time.Hour,
		SleepDefault: 30 * time.Minute,
	}, Deps{Runner: &countingRunner{count: &atomic.Int32{}}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	out, err := l.HandleSetNextSleep(t.Context(), map[string]any{"duration": "2m", "reason": "something is moving"})
	if err != nil {
		t.Fatalf("HandleSetNextSleep: %v", err)
	}
	for _, want := range []string{`"clamped":true`, `"applied":"15m"`, `"requested":"2m"`, `"min":"15m"`} {
		if !strings.Contains(out, want) {
			t.Errorf("result missing %s\n--- got ---\n%s", want, out)
		}
	}

	l.mu.Lock()
	next := l.nextSleep
	l.mu.Unlock()
	if next != 15*time.Minute {
		t.Errorf("next sleep = %v, want the clamped 15m", next)
	}
}

func TestHandleSetNextSleepRejectsLoopsThatDoNotSleep(t *testing.T) {
	l, err := New(Config{
		Name:       "dispatch",
		Task:       "Handle one request.",
		Operation:  OperationBackgroundTask,
		Completion: CompletionNone,
	}, Deps{Runner: &countingRunner{count: &atomic.Int32{}}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = l.HandleSetNextSleep(t.Context(), map[string]any{"duration": "5m"})
	if err == nil {
		t.Fatal("expected an error for a non-service loop")
	}
	// The error has to name what this loop actually is, so the model can
	// tell "wrong tool for me" from "bad argument".
	if !strings.Contains(err.Error(), "background_task") {
		t.Errorf("error should name the loop's operation, got %q", err)
	}
}

// capturingRunner records the request each iteration was prepared with,
// so a test can assert on the tool surface the model was actually given
// rather than on the builder in isolation.
type capturingRunner struct {
	mu   sync.Mutex
	reqs []Request
}

func (r *capturingRunner) Run(_ context.Context, req Request, _ StreamCallback) (*RunResponse, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	return &RunResponse{Content: "ok", Model: "test-model"}, nil
}

func (r *capturingRunner) requests() []Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Request(nil), r.reqs...)
}

// TestRunningServiceLoopIsHandedItsOwnSleepTool is the end-to-end claim
// of #1313: a real iteration of a real service loop carries a
// set_next_sleep whose advertisement names that loop's bounds. Building
// it per-iteration rather than at hydration is what keeps it honest
// after a retune moves the envelope underneath a running loop.
func TestRunningServiceLoopIsHandedItsOwnSleepTool(t *testing.T) {
	runner := &capturingRunner{}
	l, err := New(Config{
		Name:         "reservoir_watch",
		Task:         "Watch the reservoir.",
		Operation:    OperationService,
		SleepMin:     time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      1,
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish within 5s")
	}

	reqs := runner.requests()
	if len(reqs) == 0 {
		t.Fatal("runner saw no iterations")
	}
	var sleepTool *RuntimeTool
	for i, rt := range reqs[0].RuntimeTools {
		if rt.Name == SetNextSleepToolName {
			sleepTool = &reqs[0].RuntimeTools[i]
			break
		}
	}
	if sleepTool == nil {
		t.Fatalf("iteration carried no loop-scoped %s; runtime tools = %v", SetNextSleepToolName, reqs[0].RuntimeTools)
	}
	if !strings.Contains(sleepTool.Description, "1ms") || !strings.Contains(sleepTool.Description, "2ms") {
		t.Errorf("description does not name this loop's bounds: %q", sleepTool.Description)
	}
}

// TestServiceLoopRecordsItsOwnRhythm checks the lifecycle facts land on
// the Status the self-context block projects from — a loop that iterates
// must be able to say how often it has been running and how long it was
// just out.
func TestServiceLoopRecordsItsOwnRhythm(t *testing.T) {
	runner := &capturingRunner{}
	l, err := New(Config{
		Name:         "reservoir_watch",
		Task:         "Watch the reservoir.",
		Operation:    OperationService,
		SleepMin:     time.Millisecond,
		SleepMax:     2 * time.Millisecond,
		SleepDefault: time.Millisecond,
		Jitter:       Float64Ptr(0),
		MaxIter:      3,
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish within 5s")
	}

	status := l.Status()
	if status.WakesLast24h != 3 {
		t.Errorf("WakesLast24h = %d, want 3 — every iteration begun counts", status.WakesLast24h)
	}
	if status.SleptFor <= 0 {
		t.Errorf("SleptFor = %v, want the duration of the sleep that just ended", status.SleptFor)
	}
	if status.SleptPlanned <= 0 {
		t.Errorf("SleptPlanned = %v, want the duration that sleep was scheduled for", status.SleptPlanned)
	}
}
