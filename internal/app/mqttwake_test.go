package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/router"

	_ "github.com/mattn/go-sqlite3"
)

// mqttMockRunner records Run calls for assertion.
type mqttMockRunner struct {
	mu   sync.Mutex
	reqs []*agent.Request
}

func (m *mqttMockRunner) Run(_ context.Context, req *agent.Request, _ agent.StreamCallback) (*agent.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs = append(m.reqs, req)
	return &agent.Response{Content: "ok"}, nil
}

func (m *mqttMockRunner) requests() []*agent.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]*agent.Request, len(m.reqs))
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
	seed := router.LoopSeed{
		Mission:          "automation",
		QualityFloor:     "7",
		DelegationGating: "disabled",
		Instructions:     "handle this",
	}
	store.LoadConfig([]config.SubscriptionConfig{
		{Topic: "test/+/wake", Wake: &seed},
	})

	runner := &mqttMockRunner{}
	handler := mqttWakeHandler(store, runner, nil, nil)

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
	if req.Hints["source"] != "mqtt_wake" {
		t.Errorf("source hint = %q, want %q", req.Hints["source"], "mqtt_wake")
	}
	if req.Hints["mqtt_topic"] != "test/foo/wake" {
		t.Errorf("mqtt_topic hint = %q, want %q", req.Hints["mqtt_topic"], "test/foo/wake")
	}
	if req.Hints[router.HintMission] != "automation" {
		t.Errorf("mission hint = %q, want %q", req.Hints[router.HintMission], "automation")
	}
	if req.Hints[router.HintQualityFloor] != "7" {
		t.Errorf("quality_floor hint = %q, want %q", req.Hints[router.HintQualityFloor], "7")
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
	handler := mqttWakeHandler(store, runner, fallback, nil)

	handler("unmatched/topic", []byte("data"))

	if !fallbackCalled {
		t.Error("expected fallback to be called")
	}
	if reqs := runner.requests(); len(reqs) != 0 {
		t.Errorf("expected 0 requests, got %d", len(reqs))
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

func TestApplyLoopSeed(t *testing.T) {
	seed := router.LoopSeed{
		Model:            "claude-3-opus",
		QualityFloor:     "8",
		Mission:          "automation",
		LocalOnly:        "false",
		DelegationGating: "disabled",
		ExcludeTools:     []string{"shell_exec"},
		SeedTags:         []string{"homeassistant"},
		ExtraHints:       map[string]string{"custom": "value"},
	}

	req := &agent.Request{
		Hints: map[string]string{"existing": "hint"},
	}

	applyLoopSeed(&seed, req)

	if req.Model != "claude-3-opus" {
		t.Errorf("Model = %q, want %q", req.Model, "claude-3-opus")
	}
	if req.Hints[router.HintQualityFloor] != "8" {
		t.Errorf("quality_floor = %q, want %q", req.Hints[router.HintQualityFloor], "8")
	}
	if req.Hints[router.HintMission] != "automation" {
		t.Errorf("mission = %q, want %q", req.Hints[router.HintMission], "automation")
	}
	if req.Hints["existing"] != "hint" {
		t.Errorf("existing hint was overwritten")
	}
	if req.Hints["custom"] != "value" {
		t.Errorf("extra hint not applied")
	}
	if len(req.ExcludeTools) != 1 || req.ExcludeTools[0] != "shell_exec" {
		t.Errorf("ExcludeTools = %v, want [shell_exec]", req.ExcludeTools)
	}
	if len(req.SeedTags) != 1 || req.SeedTags[0] != "homeassistant" {
		t.Errorf("SeedTags = %v, want [homeassistant]", req.SeedTags)
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
