package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/anticipation"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
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

// stubStateGetter returns preconfigured entity states for testing.
type stubStateGetter struct {
	states map[string]*homeassistant.State
	calls  []string // records entity IDs fetched
}

func (s *stubStateGetter) GetState(_ context.Context, entityID string) (*homeassistant.State, error) {
	s.calls = append(s.calls, entityID)
	if st, ok := s.states[entityID]; ok {
		return st, nil
	}
	return nil, fmt.Errorf("entity not found: %s", entityID)
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

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
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
		if req.Hints[router.HintQualityFloor] != "5" {
			t.Errorf("hint quality_floor = %q, want %q", req.Hints[router.HintQualityFloor], "5")
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
		name       string
		ant        *anticipation.Anticipation
		entityID   string
		oldState   string
		newState   string
		entityCtx  string
		wantSubs   []string
		absentSubs []string
	}{
		{
			name: "with context and entity states",
			ant: &anticipation.Anticipation{
				Description: "Front door opened after dark",
				Context:     "Send a notification to Dan.",
			},
			entityID:  "binary_sensor.front_door",
			oldState:  "off",
			newState:  "on",
			entityCtx: "Entity: sensor.temp\nState: 72\n",
			wantSubs: []string{
				`"Front door opened after dark"`,
				"binary_sensor.front_door",
				`"off"`,
				`"on"`,
				"Send a notification to Dan.",
				"Instructions you left for yourself:",
				"Relevant entity states:",
				"sensor.temp",
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
			absentSubs: []string{
				"Instructions you left for yourself:",
				"Relevant entity states:",
			},
		},
		{
			name: "with context but no entity states",
			ant: &anticipation.Anticipation{
				Description: "Light turned on",
				Context:     "Check if it's daytime.",
			},
			entityID:  "light.kitchen",
			oldState:  "off",
			newState:  "on",
			entityCtx: "",
			wantSubs: []string{
				"Light turned on",
				"Instructions you left for yourself:",
			},
			absentSubs: []string{
				"Relevant entity states:",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := formatWakeMessage(tt.ant, tt.entityID, tt.oldState, tt.newState, tt.entityCtx)
			for _, sub := range tt.wantSubs {
				if !strings.Contains(msg, sub) {
					t.Errorf("message missing %q:\n%s", sub, msg)
				}
			}
			for _, sub := range tt.absentSubs {
				if strings.Contains(msg, sub) {
					t.Errorf("message should not contain %q:\n%s", sub, msg)
				}
			}
		})
	}
}

func TestFetchEntityContext_WithEntities(t *testing.T) {
	getter := &stubStateGetter{
		states: map[string]*homeassistant.State{
			"sensor.temp": {
				EntityID:   "sensor.temp",
				State:      "72",
				Attributes: map[string]any{"friendly_name": "Temperature", "unit_of_measurement": "°F"},
			},
			"light.kitchen": {
				EntityID:   "light.kitchen",
				State:      "on",
				Attributes: map[string]any{"friendly_name": "Kitchen Light", "brightness": float64(200)},
			},
			"person.dan": {
				EntityID:   "person.dan",
				State:      "home",
				Attributes: map[string]any{"friendly_name": "Dan"},
			},
		},
	}

	b := &WakeBridge{
		ha:     getter,
		logger: discardLogger(),
		ctx:    context.Background(),
	}

	a := &anticipation.Anticipation{
		ContextEntities: []string{"sensor.temp", "light.kitchen"},
	}

	result := b.fetchEntityContext(a, "person.dan")

	if !strings.Contains(result, "sensor.temp") {
		t.Error("missing sensor.temp in output")
	}
	if !strings.Contains(result, "light.kitchen") {
		t.Error("missing light.kitchen in output")
	}
	if !strings.Contains(result, "person.dan") {
		t.Error("missing trigger entity in output")
	}

	// Should have fetched 3 entities: 2 context + 1 trigger.
	if len(getter.calls) != 3 {
		t.Errorf("expected 3 GetState calls, got %d", len(getter.calls))
	}
}

func TestFetchEntityContext_Deduplication(t *testing.T) {
	getter := &stubStateGetter{
		states: map[string]*homeassistant.State{
			"person.dan": {
				EntityID:   "person.dan",
				State:      "home",
				Attributes: map[string]any{"friendly_name": "Dan"},
			},
			"sensor.temp": {
				EntityID:   "sensor.temp",
				State:      "72",
				Attributes: map[string]any{"friendly_name": "Temperature"},
			},
		},
	}

	b := &WakeBridge{
		ha:     getter,
		logger: discardLogger(),
		ctx:    context.Background(),
	}

	// Trigger entity is already in the context entities list.
	a := &anticipation.Anticipation{
		ContextEntities: []string{"person.dan", "sensor.temp"},
	}

	b.fetchEntityContext(a, "person.dan")

	// person.dan appears in both ContextEntities and as trigger — only fetch once.
	if len(getter.calls) != 2 {
		t.Errorf("expected 2 GetState calls (deduplicated), got %d: %v", len(getter.calls), getter.calls)
	}
}

func TestFetchEntityContext_NilHA(t *testing.T) {
	b := &WakeBridge{
		ha:     nil,
		logger: discardLogger(),
		ctx:    context.Background(),
	}

	a := &anticipation.Anticipation{
		ContextEntities: []string{"sensor.temp"},
	}

	result := b.fetchEntityContext(a, "person.dan")
	if result != "" {
		t.Errorf("expected empty string when HA is nil, got %q", result)
	}
}

func TestFetchEntityContext_FetchError(t *testing.T) {
	getter := &stubStateGetter{
		states: map[string]*homeassistant.State{
			"sensor.temp": {
				EntityID:   "sensor.temp",
				State:      "72",
				Attributes: map[string]any{"friendly_name": "Temperature"},
			},
		},
	}

	b := &WakeBridge{
		ha:     getter,
		logger: discardLogger(),
		ctx:    context.Background(),
	}

	// sensor.missing does not exist in the stub.
	a := &anticipation.Anticipation{
		ContextEntities: []string{"sensor.temp", "sensor.missing"},
	}

	result := b.fetchEntityContext(a, "")

	if !strings.Contains(result, "sensor.temp") {
		t.Error("missing sensor.temp in output")
	}
	if !strings.Contains(result, "sensor.missing") {
		t.Error("missing sensor.missing in output")
	}
	if !strings.Contains(result, "fetch failed") {
		t.Error("missing fetch failure note")
	}
}

func TestFetchEntityContext_NoEntities(t *testing.T) {
	getter := &stubStateGetter{
		states: map[string]*homeassistant.State{},
	}

	b := &WakeBridge{
		ha:     getter,
		logger: discardLogger(),
		ctx:    context.Background(),
	}

	a := &anticipation.Anticipation{}

	result := b.fetchEntityContext(a, "")
	if result != "" {
		t.Errorf("expected empty string with no entities, got %q", result)
	}
}
