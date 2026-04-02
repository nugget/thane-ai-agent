package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/router"
)

// mockWakeStore implements wakeTopicMatcher and wakeFireRecorder.
type mockWakeStore struct {
	subs  []*homeassistant.WakeSubscription
	fired []string
	mu    sync.Mutex
}

func (m *mockWakeStore) ActiveByTopic(topic string) ([]*homeassistant.WakeSubscription, error) {
	var result []*homeassistant.WakeSubscription
	for _, s := range m.subs {
		if s.Topic == topic {
			result = append(result, s)
		}
	}
	return result, nil
}

func (m *mockWakeStore) RecordFire(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fired = append(m.fired, id)
	return nil
}

// chanWakeRunner captures requests on a channel.
type chanWakeRunner struct {
	ch chan *agent.Request
}

func (r *chanWakeRunner) Run(_ context.Context, req *agent.Request, _ agent.StreamCallback) (*agent.Response, error) {
	r.ch <- req
	return &agent.Response{Content: "ok"}, nil
}

func TestWakeHandler_HandleMessage(t *testing.T) {
	store := &mockWakeStore{
		subs: []*homeassistant.WakeSubscription{
			{
				ID:      "wake_123",
				Topic:   "thane/test/motion",
				Name:    "Garage motion",
				KBRef:   "routines/security.md",
				Context: "Check cameras",
				Seed: router.LoopSeed{
					Source:  "wake",
					Mission: "anticipation",
				},
				Enabled: true,
			},
		},
	}

	runner := &chanWakeRunner{ch: make(chan *agent.Request, 1)}

	h := NewWakeHandler(WakeHandlerConfig{
		Store:  store,
		Fire:   store,
		Runner: runner,
	})

	h.HandleMessage("thane/test/motion", []byte(`{"entity_id":"binary_sensor.garage_motion","state":"on"}`))

	// Wait for the async wake goroutine.
	select {
	case req := <-runner.ch:
		if req.ConversationID != "wake-wake_123" {
			t.Errorf("ConversationID = %q, want %q", req.ConversationID, "wake-wake_123")
		}
		if req.Hints["source"] != "wake" {
			t.Errorf("Hints[source] = %q, want %q", req.Hints["source"], "wake")
		}
		if req.Hints["subscription_id"] != "wake_123" {
			t.Errorf("Hints[subscription_id] = %q, want %q", req.Hints["subscription_id"], "wake_123")
		}
		if len(req.Messages) != 1 {
			t.Fatalf("expected 1 message, got %d", len(req.Messages))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for wake")
	}

	// Verify fire was recorded.
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.fired) != 1 || store.fired[0] != "wake_123" {
		t.Errorf("fired = %v, want [wake_123]", store.fired)
	}
}

func TestWakeHandler_NoMatch(t *testing.T) {
	store := &mockWakeStore{subs: nil}
	runner := &chanWakeRunner{ch: make(chan *agent.Request, 1)}

	h := NewWakeHandler(WakeHandlerConfig{
		Store:  store,
		Fire:   store,
		Runner: runner,
	})

	h.HandleMessage("thane/test/unknown", []byte("test"))

	// Should not trigger any wake.
	select {
	case <-runner.ch:
		t.Fatal("unexpected wake for unmatched topic")
	case <-time.After(100 * time.Millisecond):
		// Expected: no wake triggered.
	}
}

func TestFormatWakeHandlerMessage(t *testing.T) {
	sub := &homeassistant.WakeSubscription{
		Name:    "Test subscription",
		KBRef:   "test.md",
		Context: "Do the thing",
	}

	msg := formatWakeHandlerMessage(sub, "thane/test/topic", []byte(`{"key":"value"}`))

	if msg == "" {
		t.Fatal("expected non-empty message")
	}

	checks := []string{
		"Test subscription",
		"thane/test/topic",
		`"key":"value"`,
		"test.md",
		"Do the thing",
	}
	for _, check := range checks {
		if !contains(msg, check) {
			t.Errorf("message missing %q", check)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
