package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"

	_ "github.com/mattn/go-sqlite3"
)

// mqttMockRunner records Run calls for assertion.
type mqttMockRunner struct {
	mu   sync.Mutex
	reqs []looppkg.Request
}

func (m *mqttMockRunner) Run(_ context.Context, req looppkg.Request, _ looppkg.StreamCallback) (*looppkg.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs = append(m.reqs, req)
	return &looppkg.Response{
		Content:       "ok",
		Model:         "test-model",
		InputTokens:   11,
		OutputTokens:  3,
		ContextWindow: 4096,
		RequestID:     "req-mqtt-test",
		ActiveTags:    append([]string(nil), req.InitialTags...),
	}, nil
}

func (m *mqttMockRunner) requests() []looppkg.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]looppkg.Request, len(m.reqs))
	copy(cp, m.reqs)
	return cp
}

func newTestWakeStore(t *testing.T) *mqtt.SubscriptionStore {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := mqtt.NewSubscriptionStore(db, nil)
	if err != nil {
		t.Fatalf("new subscription store: %v", err)
	}
	return store
}

func TestMQTTWakeHandlerMatchingTopic(t *testing.T) {
	store := newTestWakeStore(t)
	seed := router.LoopProfile{
		Mission:          "automation",
		QualityFloor:     7,
		DelegationGating: "disabled",
		Instructions:     "handle this",
	}
	if err := store.LoadConfig([]config.SubscriptionConfig{
		{Topic: "test/+/wake", Wake: &seed},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	runner := &mqttMockRunner{}
	registry := looppkg.NewRegistry()
	bus := events.New()

	var parentID atomic.Value
	parentID.Store("test-mqtt-parent")

	deps := mqttWakeDeps{
		registry: registry,
		eventBus: bus,
		parentID: &parentID,
	}

	handler := mqttWakeHandler(store, runner, nil, nil, deps)
	handler("test/foo/wake", []byte(`{"event": "motion"}`))

	// Wait for goroutine dispatch.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if reqs := runner.requests(); len(reqs) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	reqs := runner.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}

	req := reqs[0]
	if req.RoutingFactors["source"] != "mqtt_wake" {
		t.Errorf("source hint = %q, want %q", req.RoutingFactors["source"], "mqtt_wake")
	}
	if req.RoutingFactors["mqtt_topic"] != "test/foo/wake" {
		t.Errorf("mqtt_topic hint = %q, want %q", req.RoutingFactors["mqtt_topic"], "test/foo/wake")
	}
	if req.RoutingFactors[router.FactorMission] != "automation" {
		t.Errorf("mission hint = %q, want %q", req.RoutingFactors[router.FactorMission], "automation")
	}
	if req.RoutingFactors[router.FactorQualityFloor] != "7" {
		t.Errorf("quality_floor hint = %q, want %q", req.RoutingFactors[router.FactorQualityFloor], "7")
	}

	// Verify message contains instructions and payload.
	msg := req.Messages[0].Content
	if msg == "" {
		t.Fatal("message content is empty")
	}
	if !contains(msg, "handle this") {
		t.Errorf("message should contain instructions, got %q", msg)
	}
	if !contains(msg, "motion") {
		t.Errorf("message should contain payload, got %q", msg)
	}
}

func TestMQTTWakeHandlerNoMatchFallback(t *testing.T) {
	store := newTestWakeStore(t)

	var fallbackCalled bool
	fallback := func(topic string, payload []byte) {
		fallbackCalled = true
	}

	runner := &mqttMockRunner{}
	handler := mqttWakeHandler(store, runner, fallback, nil, mqttWakeDeps{})

	handler("unmatched/topic", []byte("data"))

	if !fallbackCalled {
		t.Error("expected fallback to be called")
	}
	if reqs := runner.requests(); len(reqs) != 0 {
		t.Errorf("expected 0 requests, got %d", len(reqs))
	}
}

func TestMQTTWakeHandlerWithRegistry(t *testing.T) {
	store := newTestWakeStore(t)
	seed := router.LoopProfile{Instructions: "test instructions"}
	if err := store.LoadConfig([]config.SubscriptionConfig{
		{Topic: "home/sensor/wake", Wake: &seed},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	runner := &mqttMockRunner{}
	registry := looppkg.NewRegistry()
	bus := events.New()

	var parentID atomic.Value
	parentID.Store("parent-loop-123")

	deps := mqttWakeDeps{
		registry: registry,
		eventBus: bus,
		parentID: &parentID,
	}

	handler := mqttWakeHandler(store, runner, nil, nil, deps)
	handler("home/sensor/wake", []byte(`{"temp": 22}`))

	// Wait for the spawned loop to complete.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if reqs := runner.requests(); len(reqs) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	reqs := runner.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request via registry dispatch, got %d", len(reqs))
	}

	req := reqs[0]
	if req.RoutingFactors["source"] != "mqtt_wake" {
		t.Errorf("source hint = %q, want %q", req.RoutingFactors["source"], "mqtt_wake")
	}
	if req.RoutingFactors["loop_id"] == "" {
		t.Error("loop_id hint is empty; request did not traverse loop turn preparation")
	}
	if req.RoutingFactors["loop_name"] != "mqtt/wake" {
		t.Errorf("loop_name hint = %q, want %q", req.RoutingFactors["loop_name"], "mqtt/wake")
	}
}

func TestMQTTWakeHandlerFanOut(t *testing.T) {
	store := newTestWakeStore(t)
	seedA := router.LoopProfile{Mission: "automation", Instructions: "check temperature"}
	seedB := router.LoopProfile{Mission: "background", Instructions: "log reading"}
	if err := store.LoadConfig([]config.SubscriptionConfig{
		{Topic: "sensors/+/reading", Wake: &seedA},
		{Topic: "sensors/+/reading", Wake: &seedB},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	runner := &mqttMockRunner{}
	registry := looppkg.NewRegistry()
	bus := events.New()

	var parentID atomic.Value
	parentID.Store("test-mqtt-parent")

	deps := mqttWakeDeps{
		registry: registry,
		eventBus: bus,
		parentID: &parentID,
	}

	handler := mqttWakeHandler(store, runner, nil, nil, deps)
	handler("sensors/temp/reading", []byte(`{"value": 22.5}`))

	// Wait for both goroutines to dispatch.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if reqs := runner.requests(); len(reqs) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	reqs := runner.requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests (fan-out), got %d", len(reqs))
	}

	// Both should have the subscription ID hint set.
	for i, req := range reqs {
		if req.RoutingFactors["mqtt_subscription_id"] == "" {
			t.Errorf("request %d missing mqtt_subscription_id hint", i)
		}
	}
}

func TestMQTTWakeHandlerNoRegistryDropsMessage(t *testing.T) {
	store := newTestWakeStore(t)
	seed := router.LoopProfile{Mission: "automation"}
	if err := store.LoadConfig([]config.SubscriptionConfig{
		{Topic: "test/topic", Wake: &seed},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	runner := &mqttMockRunner{}
	// No registry — message should be dropped, not dispatched directly.
	handler := mqttWakeHandler(store, runner, nil, nil, mqttWakeDeps{})
	handler("test/topic", []byte("data"))

	// Give goroutine time to (not) run.
	time.Sleep(200 * time.Millisecond)

	if reqs := runner.requests(); len(reqs) != 0 {
		t.Fatalf("expected 0 requests without registry, got %d", len(reqs))
	}
}

// TestMQTTWakeHandlerDispatchesViaWakeTarget pins the post-PR-T1
// trigger-unification contract: a subscription with a WakeTarget
// configured delivers matching messages as event-source envelopes
// to the target loop, instead of spawning a fresh one-shot loop.
// The bus audit hook captures the envelope so we can verify shape.
func TestMQTTWakeHandlerDispatchesViaWakeTarget(t *testing.T) {
	store := newTestWakeStore(t)

	// Operator declares "messages on frigate/+/events go to the
	// home_security loop." No spawn-profile fields: the target
	// loop owns routing.
	target := messages.LoopWakeTarget{Name: "home_security", Priority: messages.PriorityNormal}
	if err := store.LoadConfig([]config.SubscriptionConfig{
		{Topic: "frigate/+/events", WakeLoop: &target},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	bus := messages.NewBus(nil)
	// Capture the envelope the dispatcher sends.
	var captured []messages.Envelope
	var capturedMu sync.Mutex
	bus.AddAuditFunc(func(_ context.Context, env messages.Envelope, _ *messages.DeliveryResult, _ error) {
		capturedMu.Lock()
		defer capturedMu.Unlock()
		captured = append(captured, env)
	})
	// Stub the loop destination handler so Send doesn't fail on
	// the unrouted loop — we don't have a live registry here.
	bus.RegisterRoute(messages.DestinationLoop, func(_ context.Context, env messages.Envelope) (messages.DeliveryResult, error) {
		return messages.DeliveryResult{Envelope: env, Status: messages.DeliveryDelivered}, nil
	})

	runner := &mqttMockRunner{}
	registry := looppkg.NewRegistry()
	deps := mqttWakeDeps{
		registry:   registry,
		messageBus: bus,
		eventBus:   events.New(),
	}

	handler := mqttWakeHandler(store, runner, nil, nil, deps)
	handler("frigate/front/events", []byte(`{"event":"motion"}`))

	// Give the dispatcher goroutine time to run.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		capturedMu.Lock()
		if len(captured) > 0 {
			capturedMu.Unlock()
			break
		}
		capturedMu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}

	capturedMu.Lock()
	defer capturedMu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("expected 1 envelope delivered, got %d", len(captured))
	}
	env := captured[0]
	if env.To.Target != "home_security" {
		t.Errorf("envelope target = %q, want %q", env.To.Target, "home_security")
	}
	if env.Type != messages.TypeSignal {
		t.Errorf("envelope type = %q, want %q", env.Type, messages.TypeSignal)
	}
	payload, ok := env.Payload.(messages.LoopNotifyPayload)
	if !ok {
		t.Fatalf("payload type = %T, want messages.LoopNotifyPayload", env.Payload)
	}
	if payload.Kind != "event_source" {
		t.Errorf("payload.Kind = %q, want %q", payload.Kind, "event_source")
	}
	if len(payload.Events) != 1 {
		t.Fatalf("payload.Events len = %d, want 1", len(payload.Events))
	}
	event := payload.Events[0]
	if event.Source != "mqtt_wake" {
		t.Errorf("event.Source = %q, want %q", event.Source, "mqtt_wake")
	}
	if event.Title != "frigate/front/events" {
		t.Errorf("event.Title = %q, want the topic", event.Title)
	}

	// And critically: no agent run happened. The whole point of
	// the wake-target path is that the existing target loop sees
	// the event on its next iteration; no new conversation
	// spawns.
	if reqs := runner.requests(); len(reqs) != 0 {
		t.Errorf("expected 0 spawned agent runs, got %d (wake-target dispatch should not spawn)", len(reqs))
	}
}

// TestMQTTWakeHandlerFallsBackToSpawnWithoutWakeTarget guards the
// backwards-compat path: a subscription with no WakeTarget but a
// legacy Profile still uses the spawn-per-message dispatch.
func TestMQTTWakeHandlerFallsBackToSpawnWithoutWakeTarget(t *testing.T) {
	store := newTestWakeStore(t)
	seed := router.LoopProfile{Mission: "automation"}
	if err := store.LoadConfig([]config.SubscriptionConfig{
		{Topic: "legacy/topic", Wake: &seed},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	bus := messages.NewBus(nil)
	var capturedMu sync.Mutex
	var captured []messages.Envelope
	bus.AddAuditFunc(func(_ context.Context, env messages.Envelope, _ *messages.DeliveryResult, _ error) {
		capturedMu.Lock()
		defer capturedMu.Unlock()
		captured = append(captured, env)
	})

	runner := &mqttMockRunner{}
	registry := looppkg.NewRegistry()
	var parentID atomic.Value
	parentID.Store("test-parent")
	deps := mqttWakeDeps{
		registry:   registry,
		messageBus: bus,
		eventBus:   events.New(),
		parentID:   &parentID,
	}

	handler := mqttWakeHandler(store, runner, nil, nil, deps)
	handler("legacy/topic", []byte("data"))

	time.Sleep(200 * time.Millisecond)

	// Spawn path uses SpawnLoop, which won't find the fake
	// parent_id in the registry — but the important assertion is
	// that no envelope went through the bus (that's the
	// wake-target path).
	capturedMu.Lock()
	defer capturedMu.Unlock()
	if len(captured) != 0 {
		t.Errorf("expected 0 bus envelopes for legacy spawn path, got %d", len(captured))
	}
}

func TestBuildWakeMessage(t *testing.T) {
	tests := []struct {
		name         string
		topic        string
		payload      []byte
		instructions string
		wantContains []string
	}{
		{
			name:         "no instructions",
			topic:        "test/topic",
			payload:      []byte("hello"),
			wantContains: []string{"test/topic", "hello"},
		},
		{
			name:         "with instructions",
			topic:        "test/topic",
			payload:      []byte("hello"),
			instructions: "do something",
			wantContains: []string{"Instructions: do something", "test/topic", "hello"},
		},
		{
			name:         "empty payload no instructions",
			topic:        "test/topic",
			payload:      nil,
			wantContains: []string{"test/topic"},
		},
		{
			name:         "empty payload with instructions",
			topic:        "test/topic",
			payload:      nil,
			instructions: "check status",
			wantContains: []string{"check status", "test/topic", "no payload"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := buildWakeMessage(tt.topic, tt.payload, tt.instructions)
			for _, s := range tt.wantContains {
				if !contains(msg, s) {
					t.Errorf("message %q should contain %q", msg, s)
				}
			}
		})
	}
}

func TestApplyLoopProfile(t *testing.T) {
	seed := router.LoopProfile{
		Model:            "claude-3-opus",
		QualityFloor:     8,
		Mission:          "automation",
		LocalOnly:        "false",
		DelegationGating: "disabled",
		ExcludeTools:     []string{"shell_exec"},
		ExtraHints:       map[string]string{"custom": "value"},
	}

	req := &looppkg.Request{
		RoutingFactors: map[string]string{"existing": "hint"},
		ExcludeTools:   []string{"files_read"},
		InitialTags:    []string{"baseline"},
	}

	applyLoopProfile(&seed, req)

	if req.Model != "claude-3-opus" {
		t.Errorf("Model = %q, want %q", req.Model, "claude-3-opus")
	}
	if req.RoutingFactors[router.FactorQualityFloor] != "8" {
		t.Errorf("quality_floor = %q, want %q", req.RoutingFactors[router.FactorQualityFloor], "8")
	}
	if req.RoutingFactors[router.FactorMission] != "automation" {
		t.Errorf("mission = %q, want %q", req.RoutingFactors[router.FactorMission], "automation")
	}
	if req.RoutingFactors["existing"] != "hint" {
		t.Errorf("existing hint was overwritten")
	}
	if req.RoutingFactors["custom"] != "value" {
		t.Errorf("extra hint not applied")
	}
	if len(req.ExcludeTools) != 2 || req.ExcludeTools[0] != "files_read" || req.ExcludeTools[1] != "shell_exec" {
		t.Errorf("ExcludeTools = %v, want [files_read shell_exec]", req.ExcludeTools)
	}
	// applyLoopProfile is purely routing/configuration; capability tags
	// are no longer part of LoopProfile and are applied separately by
	// the caller (e.g., from WakeSubscription.InitialTags).
	if len(req.InitialTags) != 1 || req.InitialTags[0] != "baseline" {
		t.Errorf("InitialTags = %v, want [baseline] (profile must not touch tags)", req.InitialTags)
	}
}

// contains is a test helper for string containment checks.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(substr) <= len(s) && searchString(s, substr)))
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
