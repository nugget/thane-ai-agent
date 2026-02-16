package homeassistant

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestEntityFilter_Match(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		entityID string
		want     bool
	}{
		{"empty patterns match all", nil, "light.kitchen", true},
		{"exact match", []string{"light.kitchen"}, "light.kitchen", true},
		{"glob star", []string{"person.*"}, "person.dan", true},
		{"glob star no match", []string{"person.*"}, "light.kitchen", false},
		{"wildcard in middle", []string{"binary_sensor.*door*"}, "binary_sensor.front_door", true},
		{"wildcard in middle no match", []string{"binary_sensor.*door*"}, "binary_sensor.motion", false},
		{"multiple patterns first match", []string{"person.*", "light.*"}, "person.dan", true},
		{"multiple patterns second match", []string{"person.*", "light.*"}, "light.kitchen", true},
		{"multiple patterns no match", []string{"person.*", "light.*"}, "switch.garage", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewEntityFilter(tt.patterns, nil)
			got := f.Match(tt.entityID)
			if got != tt.want {
				t.Errorf("Match(%q) = %v, want %v", tt.entityID, got, tt.want)
			}
		})
	}
}

func TestEntityFilter_EmptyMatchesAll(t *testing.T) {
	f := NewEntityFilter([]string{}, nil)
	entities := []string{"person.dan", "light.kitchen", "binary_sensor.door", "switch.garage"}
	for _, e := range entities {
		if !f.Match(e) {
			t.Errorf("empty filter should match %q", e)
		}
	}
}

func TestEntityRateLimiter_Allow(t *testing.T) {
	limiter := NewEntityRateLimiter(3)

	// First 3 should be allowed.
	for i := range 3 {
		if !limiter.Allow("light.kitchen") {
			t.Errorf("call %d should be allowed", i+1)
		}
	}

	// 4th should be blocked.
	if limiter.Allow("light.kitchen") {
		t.Error("call 4 should be blocked")
	}

	// Different entity should still be allowed.
	if !limiter.Allow("light.bedroom") {
		t.Error("different entity should be allowed")
	}
}

func TestEntityRateLimiter_Disabled(t *testing.T) {
	limiter := NewEntityRateLimiter(0)

	for range 100 {
		if !limiter.Allow("light.kitchen") {
			t.Fatal("disabled limiter should always allow")
		}
	}
}

func TestEntityRateLimiter_WindowExpiry(t *testing.T) {
	limiter := NewEntityRateLimiter(2)
	// Override the window to a short duration for testing.
	limiter.window = 50 * time.Millisecond

	if !limiter.Allow("light.a") {
		t.Fatal("first call should be allowed")
	}
	if !limiter.Allow("light.a") {
		t.Fatal("second call should be allowed")
	}
	if limiter.Allow("light.a") {
		t.Fatal("third call should be blocked")
	}

	// Wait for the window to expire.
	time.Sleep(60 * time.Millisecond)

	if !limiter.Allow("light.a") {
		t.Fatal("call after window expiry should be allowed")
	}
}

func TestEntityRateLimiter_Cleanup(t *testing.T) {
	limiter := NewEntityRateLimiter(5)
	limiter.window = 50 * time.Millisecond

	// Add entries for two entities.
	limiter.Allow("light.a")
	limiter.Allow("light.b")

	// Both should have counter entries.
	limiter.mu.Lock()
	if len(limiter.counters) != 2 {
		t.Fatalf("expected 2 counter entries, got %d", len(limiter.counters))
	}
	limiter.mu.Unlock()

	// Wait for the window to expire.
	time.Sleep(60 * time.Millisecond)

	limiter.Cleanup()

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if len(limiter.counters) != 0 {
		t.Errorf("expected 0 counter entries after cleanup, got %d", len(limiter.counters))
	}
}

func TestStateWatcher_Run(t *testing.T) {
	events := make(chan Event, 10)

	var mu sync.Mutex
	var received []struct {
		entityID, oldState, newState string
	}

	handler := func(entityID, oldState, newState string) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, struct {
			entityID, oldState, newState string
		}{entityID, oldState, newState})
	}

	filter := NewEntityFilter([]string{"light.*"}, nil)
	limiter := NewEntityRateLimiter(0)
	watcher := NewStateWatcher(events, filter, limiter, handler, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		watcher.Run(ctx)
		close(done)
	}()

	// Send a matching event.
	events <- makeStateEvent(t, "light.kitchen", "off", "on")

	// Send a non-matching event (should be filtered out).
	events <- makeStateEvent(t, "switch.garage", "off", "on")

	// Send another matching event.
	events <- makeStateEvent(t, "light.bedroom", "on", "off")

	// Give the watcher time to process.
	time.Sleep(50 * time.Millisecond)

	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()

	if len(received) != 2 {
		t.Fatalf("received %d events, want 2", len(received))
	}

	if received[0].entityID != "light.kitchen" {
		t.Errorf("event 0 entity = %q, want %q", received[0].entityID, "light.kitchen")
	}
	if received[0].oldState != "off" || received[0].newState != "on" {
		t.Errorf("event 0 states = %q→%q, want off→on", received[0].oldState, received[0].newState)
	}

	if received[1].entityID != "light.bedroom" {
		t.Errorf("event 1 entity = %q, want %q", received[1].entityID, "light.bedroom")
	}
}

func TestStateWatcher_NilNewStateSkipped(t *testing.T) {
	events := make(chan Event, 10)

	called := false
	handler := func(_, _, _ string) { called = true }

	watcher := NewStateWatcher(events, nil, nil, handler, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		watcher.Run(ctx)
		close(done)
	}()

	// Send an event with nil NewState (entity removal).
	data := StateChangedData{
		EntityID: "light.removed",
		OldState: &State{State: "on"},
		NewState: nil,
	}
	raw, _ := json.Marshal(data)
	events <- Event{Type: "state_changed", Data: raw}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if called {
		t.Error("handler should not be called for nil NewState")
	}
}

func TestStateWatcher_NonStateChangedIgnored(t *testing.T) {
	events := make(chan Event, 10)

	called := false
	handler := func(_, _, _ string) { called = true }

	watcher := NewStateWatcher(events, nil, nil, handler, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		watcher.Run(ctx)
		close(done)
	}()

	// Send a non-state_changed event.
	events <- Event{Type: "automation_triggered", Data: json.RawMessage(`{}`)}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if called {
		t.Error("handler should not be called for non-state_changed events")
	}
}

// makeStateEvent creates a state_changed Event for testing.
func makeStateEvent(t *testing.T, entityID, oldState, newState string) Event {
	t.Helper()
	data := StateChangedData{
		EntityID: entityID,
		OldState: &State{State: oldState},
		NewState: &State{State: newState},
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal state data: %v", err)
	}
	return Event{Type: "state_changed", Data: raw}
}
