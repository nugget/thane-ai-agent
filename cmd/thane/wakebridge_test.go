package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/anticipation"
	"github.com/nugget/thane-ai-agent/internal/router"
)

// mockMatcher records Match calls and returns canned results.
type mockMatcher struct {
	matched []*anticipation.Anticipation
	err     error
}

func (m *mockMatcher) Match(_ anticipation.WakeContext) ([]*anticipation.Anticipation, error) {
	return m.matched, m.err
}

// chanRunner is a goroutine-safe mock runner that sends received
// requests to a channel for test synchronization.
type chanRunner struct {
	calls chan *agent.Request
	resp  *agent.Response
	err   error
}

func (r *chanRunner) Run(_ context.Context, req *agent.Request, _ agent.StreamCallback) (*agent.Response, error) {
	r.calls <- req
	return r.resp, r.err
}

// mockProvider records SetWakeContext calls.
type mockProvider struct {
	lastCtx anticipation.WakeContext
	called  bool
}

func (p *mockProvider) SetWakeContext(ctx anticipation.WakeContext) {
	p.lastCtx = ctx
	p.called = true
}

func newTestBridge(matcher anticipationMatcher, runner agentRunner, cooldown time.Duration) (*WakeBridge, *mockProvider) {
	provider := &mockProvider{}
	b := NewWakeBridge(WakeBridgeConfig{
		Store:    matcher,
		Runner:   runner,
		Provider: provider,
		Logger:   slog.Default(),
		Ctx:      context.Background(),
		Cooldown: cooldown,
	})
	return b, provider
}

func TestWakeBridge_MatchTriggersRun(t *testing.T) {
	matcher := &mockMatcher{
		matched: []*anticipation.Anticipation{{
			ID:          "ant-1",
			Description: "Front door opened",
			Context:     "Check if anyone is home.",
		}},
	}
	runner := &chanRunner{
		calls: make(chan *agent.Request, 1),
		resp:  &agent.Response{Content: "Checked."},
	}
	bridge, provider := newTestBridge(matcher, runner, time.Hour)

	bridge.HandleStateChange("binary_sensor.front_door", "off", "on")

	select {
	case req := <-runner.calls:
		if len(req.Messages) != 1 {
			t.Fatalf("expected 1 message, got %d", len(req.Messages))
		}
		if !strings.Contains(req.Messages[0].Content, "Front door opened") {
			t.Errorf("message missing anticipation description: %q", req.Messages[0].Content)
		}
		if !strings.Contains(req.Messages[0].Content, "binary_sensor.front_door") {
			t.Errorf("message missing entity ID: %q", req.Messages[0].Content)
		}
		if !strings.Contains(req.Messages[0].Content, "Check if anyone is home.") {
			t.Errorf("message missing context: %q", req.Messages[0].Content)
		}

		// Verify routing hints.
		if req.Hints["source"] != "anticipation" {
			t.Errorf("hint source = %q, want %q", req.Hints["source"], "anticipation")
		}
		if req.Hints["anticipation_id"] != "ant-1" {
			t.Errorf("hint anticipation_id = %q, want %q", req.Hints["anticipation_id"], "ant-1")
		}
		if req.Hints[router.HintLocalOnly] != "true" {
			t.Errorf("hint local_only = %q, want %q", req.Hints[router.HintLocalOnly], "true")
		}
		if req.Hints[router.HintQualityFloor] != "1" {
			t.Errorf("hint quality_floor = %q, want %q", req.Hints[router.HintQualityFloor], "1")
		}
		if req.Hints[router.HintMission] != "anticipation" {
			t.Errorf("hint mission = %q, want %q", req.Hints[router.HintMission], "anticipation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner.Run was not called within timeout")
	}

	// Verify provider was called.
	if !provider.called {
		t.Error("provider.SetWakeContext was not called")
	}
	if provider.lastCtx.EntityID != "binary_sensor.front_door" {
		t.Errorf("provider context entity = %q, want %q", provider.lastCtx.EntityID, "binary_sensor.front_door")
	}
}

func TestWakeBridge_SameStateSuppressed(t *testing.T) {
	matcher := &mockMatcher{
		matched: []*anticipation.Anticipation{{
			ID:          "ant-suppress",
			Description: "Should not fire",
		}},
	}
	runner := &chanRunner{
		calls: make(chan *agent.Request, 1),
		resp:  &agent.Response{Content: "ok"},
	}
	bridge, provider := newTestBridge(matcher, runner, time.Hour)

	// Same old and new state — should be suppressed entirely.
	bridge.HandleStateChange("person.nugget", "home", "home")

	time.Sleep(50 * time.Millisecond)

	select {
	case <-runner.calls:
		t.Error("runner should not be called when state hasn't changed")
	default:
		// Expected: no call.
	}

	// Provider should NOT be updated for no-change events.
	if provider.called {
		t.Error("provider.SetWakeContext should not be called when state hasn't changed")
	}
}

func TestWakeBridge_NoMatchNoRun(t *testing.T) {
	matcher := &mockMatcher{matched: nil}
	runner := &chanRunner{
		calls: make(chan *agent.Request, 1),
		resp:  &agent.Response{Content: "ok"},
	}
	bridge, _ := newTestBridge(matcher, runner, time.Hour)

	bridge.HandleStateChange("light.kitchen", "off", "on")

	// Give goroutine time to fire if it incorrectly does.
	time.Sleep(50 * time.Millisecond)

	select {
	case <-runner.calls:
		t.Error("runner.Run should not be called when no anticipations match")
	default:
		// Expected: no call.
	}
}

func TestWakeBridge_CooldownPreventsRetrigger(t *testing.T) {
	matcher := &mockMatcher{
		matched: []*anticipation.Anticipation{{
			ID:          "ant-cool",
			Description: "Test cooldown",
		}},
	}
	runner := &chanRunner{
		calls: make(chan *agent.Request, 2),
		resp:  &agent.Response{Content: "ok"},
	}
	bridge, _ := newTestBridge(matcher, runner, time.Hour) // Long cooldown

	// First call should trigger.
	bridge.HandleStateChange("light.a", "off", "on")
	select {
	case <-runner.calls:
		// Expected.
	case <-time.After(2 * time.Second):
		t.Fatal("first call should trigger runner")
	}

	// Second call within cooldown should NOT trigger.
	bridge.HandleStateChange("light.a", "on", "off")

	time.Sleep(50 * time.Millisecond)
	select {
	case <-runner.calls:
		t.Error("second call within cooldown should not trigger runner")
	default:
		// Expected.
	}
}

func TestWakeBridge_CooldownExpires(t *testing.T) {
	matcher := &mockMatcher{
		matched: []*anticipation.Anticipation{{
			ID:          "ant-expire",
			Description: "Test expiry",
		}},
	}
	runner := &chanRunner{
		calls: make(chan *agent.Request, 2),
		resp:  &agent.Response{Content: "ok"},
	}
	bridge, _ := newTestBridge(matcher, runner, 50*time.Millisecond) // Short cooldown

	// First call.
	bridge.HandleStateChange("light.a", "off", "on")
	select {
	case <-runner.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("first call should trigger runner")
	}

	// Wait for cooldown to expire.
	time.Sleep(60 * time.Millisecond)

	// Second call should trigger after cooldown expires.
	bridge.HandleStateChange("light.a", "on", "off")
	select {
	case <-runner.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("second call after cooldown should trigger runner")
	}
}

func TestWakeBridge_MatchErrorContinues(t *testing.T) {
	matcher := &mockMatcher{err: errors.New("database error")}
	runner := &chanRunner{
		calls: make(chan *agent.Request, 1),
		resp:  &agent.Response{Content: "ok"},
	}
	bridge, _ := newTestBridge(matcher, runner, time.Hour)

	// Should not panic.
	bridge.HandleStateChange("light.a", "off", "on")

	time.Sleep(50 * time.Millisecond)
	select {
	case <-runner.calls:
		t.Error("runner should not be called when matcher returns error")
	default:
		// Expected.
	}
}

func TestWakeBridge_RunnerErrorContinues(t *testing.T) {
	matcher := &mockMatcher{
		matched: []*anticipation.Anticipation{{
			ID:          "ant-err",
			Description: "Runner will fail",
		}},
	}
	runner := &chanRunner{
		calls: make(chan *agent.Request, 1),
		resp:  nil,
		err:   errors.New("LLM unavailable"),
	}
	bridge, _ := newTestBridge(matcher, runner, time.Hour)

	// Should not panic even though runner returns an error.
	bridge.HandleStateChange("light.a", "off", "on")

	select {
	case <-runner.calls:
		// Expected: runner was called, error handled internally.
	case <-time.After(2 * time.Second):
		t.Fatal("runner should still be called even if it will fail")
	}
}

func TestWakeBridge_MultipleMatches(t *testing.T) {
	matcher := &mockMatcher{
		matched: []*anticipation.Anticipation{
			{ID: "ant-a", Description: "First"},
			{ID: "ant-b", Description: "Second"},
		},
	}
	runner := &chanRunner{
		calls: make(chan *agent.Request, 2),
		resp:  &agent.Response{Content: "ok"},
	}
	bridge, _ := newTestBridge(matcher, runner, time.Hour)

	bridge.HandleStateChange("person.dan", "away", "home")

	// Should receive two calls.
	for i := range 2 {
		select {
		case <-runner.calls:
			// Expected.
		case <-time.After(2 * time.Second):
			t.Fatalf("expected runner call %d of 2, timed out", i+1)
		}
	}

	// No third call.
	time.Sleep(50 * time.Millisecond)
	select {
	case <-runner.calls:
		t.Error("unexpected third runner call")
	default:
	}
}

func TestFormatWakeMessage(t *testing.T) {
	tests := []struct {
		name     string
		ant      *anticipation.Anticipation
		entityID string
		oldState string
		newState string
		wantSubs []string
	}{
		{
			name: "with context",
			ant: &anticipation.Anticipation{
				Description: "Front door opened after dark",
				Context:     "Send a notification to Dan.",
			},
			entityID: "binary_sensor.front_door",
			oldState: "off",
			newState: "on",
			wantSubs: []string{
				`"Front door opened after dark"`,
				"binary_sensor.front_door",
				`"off"`,
				`"on"`,
				"Send a notification to Dan.",
				"Instructions you left for yourself:",
			},
		},
		{
			name: "without context",
			ant: &anticipation.Anticipation{
				Description: "Dan arrived home",
				Context:     "",
			},
			entityID: "person.dan",
			oldState: "not_home",
			newState: "home",
			wantSubs: []string{
				`"Dan arrived home"`,
				"person.dan",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := formatWakeMessage(tt.ant, tt.entityID, tt.oldState, tt.newState)
			for _, sub := range tt.wantSubs {
				if !strings.Contains(msg, sub) {
					t.Errorf("message missing %q:\n%s", sub, msg)
				}
			}
		})
	}
}
