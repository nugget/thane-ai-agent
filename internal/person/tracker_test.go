package person

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
)

type mockStateGetter struct {
	states map[string]*homeassistant.State
	err    error
}

func (m *mockStateGetter) GetState(_ context.Context, entityID string) (*homeassistant.State, error) {
	if m.err != nil {
		return nil, m.err
	}
	s, ok := m.states[entityID]
	if !ok {
		return nil, fmt.Errorf("entity not found: %s", entityID)
	}
	return s, nil
}

func TestNewTracker(t *testing.T) {
	tracker := NewTracker([]string{"person.alice", "person.bob"}, "America/Chicago", nil)

	ids := tracker.EntityIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 entity IDs, got %d", len(ids))
	}
	if ids[0] != "person.alice" || ids[1] != "person.bob" {
		t.Errorf("unexpected entity IDs: %v", ids)
	}

	// All entities should start as Unknown.
	ctx := context.Background()
	result, err := tracker.GetContext(ctx, "")
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}
	if !strings.Contains(result, "- Unknown") {
		t.Errorf("expected Unknown state in initial context, got:\n%s", result)
	}
}

func TestTracker_Initialize(t *testing.T) {
	now := time.Date(2026, 2, 15, 16, 30, 0, 0, time.UTC)
	getter := &mockStateGetter{
		states: map[string]*homeassistant.State{
			"person.alice": {
				EntityID:    "person.alice",
				State:       "home",
				Attributes:  map[string]any{"friendly_name": "Alice"},
				LastChanged: now,
			},
			"person.bob": {
				EntityID:    "person.bob",
				State:       "not_home",
				Attributes:  map[string]any{"friendly_name": "Bob"},
				LastChanged: now.Add(-2 * time.Hour),
			},
		},
	}

	tracker := NewTracker([]string{"person.alice", "person.bob"}, "UTC", nil)
	err := tracker.Initialize(context.Background(), getter)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	result, err := tracker.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}

	if !strings.Contains(result, "- **Alice**:") {
		t.Errorf("expected Alice in context, got:\n%s", result)
	}
	if !strings.Contains(result, "Home since") {
		t.Errorf("expected Home since in context, got:\n%s", result)
	}
	if !strings.Contains(result, "- **Bob**:") {
		t.Errorf("expected Bob in context, got:\n%s", result)
	}
	if !strings.Contains(result, "Away since") {
		t.Errorf("expected Away since in context, got:\n%s", result)
	}
}

func TestTracker_Initialize_PartialFailure(t *testing.T) {
	now := time.Date(2026, 2, 15, 16, 30, 0, 0, time.UTC)
	getter := &mockStateGetter{
		states: map[string]*homeassistant.State{
			"person.alice": {
				EntityID:    "person.alice",
				State:       "home",
				Attributes:  map[string]any{"friendly_name": "Alice"},
				LastChanged: now,
			},
			// person.bob is missing — will fail.
		},
	}

	tracker := NewTracker([]string{"person.alice", "person.bob"}, "UTC", nil)
	err := tracker.Initialize(context.Background(), getter)
	if err == nil {
		t.Fatal("expected error for partial failure")
	}

	result, _ := tracker.GetContext(context.Background(), "")

	// Alice should be populated with nested format.
	if !strings.Contains(result, "- **Alice**:\n  - Home since") {
		t.Errorf("expected Alice: Home since ..., got:\n%s", result)
	}
	// Bob should show Unknown.
	if !strings.Contains(result, "- **Bob**:\n  - Unknown") {
		t.Errorf("expected Bob: Unknown, got:\n%s", result)
	}
}

func TestTracker_HandleStateChange(t *testing.T) {
	now := time.Date(2026, 2, 15, 16, 30, 0, 0, time.UTC)
	getter := &mockStateGetter{
		states: map[string]*homeassistant.State{
			"person.alice": {
				EntityID:    "person.alice",
				State:       "home",
				Attributes:  map[string]any{"friendly_name": "Alice"},
				LastChanged: now,
			},
		},
	}

	tracker := NewTracker([]string{"person.alice"}, "UTC", nil)
	_ = tracker.Initialize(context.Background(), getter)

	// State changes to not_home.
	tracker.HandleStateChange("person.alice", "home", "not_home")

	result, _ := tracker.GetContext(context.Background(), "")
	if !strings.Contains(result, "Away since") {
		t.Errorf("expected Away since after state change, got:\n%s", result)
	}
}

func TestTracker_HandleStateChange_IgnoresUntracked(t *testing.T) {
	tracker := NewTracker([]string{"person.alice"}, "UTC", nil)

	// Should not panic or affect tracked entities.
	tracker.HandleStateChange("person.unknown", "home", "not_home")
	tracker.HandleStateChange("light.kitchen", "off", "on")

	result, _ := tracker.GetContext(context.Background(), "")
	if !strings.Contains(result, "- **Alice**:\n  - Unknown") {
		t.Errorf("expected Alice: Unknown unchanged, got:\n%s", result)
	}
}

func TestTracker_HandleStateChange_SameState(t *testing.T) {
	now := time.Date(2026, 2, 15, 16, 30, 0, 0, time.UTC)
	getter := &mockStateGetter{
		states: map[string]*homeassistant.State{
			"person.alice": {
				EntityID:    "person.alice",
				State:       "home",
				Attributes:  map[string]any{"friendly_name": "Alice"},
				LastChanged: now,
			},
		},
	}

	tracker := NewTracker([]string{"person.alice"}, "UTC", nil)
	_ = tracker.Initialize(context.Background(), getter)

	// Same state — Since should NOT update.
	tracker.HandleStateChange("person.alice", "home", "home")

	tracker.mu.RLock()
	p := tracker.people["person.alice"]
	since := p.Since
	tracker.mu.RUnlock()

	if !since.Equal(now) {
		t.Errorf("expected Since unchanged at %v, got %v", now, since)
	}
}

func TestTracker_HandleStateChange_ClearsRoom(t *testing.T) {
	now := time.Date(2026, 2, 15, 16, 30, 0, 0, time.UTC)
	getter := &mockStateGetter{
		states: map[string]*homeassistant.State{
			"person.alice": {
				EntityID:    "person.alice",
				State:       "home",
				Attributes:  map[string]any{"friendly_name": "Alice"},
				LastChanged: now,
			},
		},
	}

	tracker := NewTracker([]string{"person.alice"}, "UTC", nil)
	_ = tracker.Initialize(context.Background(), getter)

	// Set a room.
	tracker.UpdateRoom("person.alice", "office", "ap-hor-office")

	result, _ := tracker.GetContext(context.Background(), "")
	if !strings.Contains(result, "Room: office") {
		t.Errorf("expected Room: office, got:\n%s", result)
	}

	// Transition to not_home should clear room.
	tracker.HandleStateChange("person.alice", "home", "not_home")

	result, _ = tracker.GetContext(context.Background(), "")
	if strings.Contains(result, "Room:") {
		t.Errorf("expected room cleared after not_home, got:\n%s", result)
	}
}

func TestTracker_GetContext(t *testing.T) {
	chicago, _ := time.LoadLocation("America/Chicago")
	now := time.Date(2026, 2, 15, 16, 30, 0, 0, chicago)

	getter := &mockStateGetter{
		states: map[string]*homeassistant.State{
			"person.alice": {
				EntityID:    "person.alice",
				State:       "home",
				Attributes:  map[string]any{"friendly_name": "Alice"},
				LastChanged: now,
			},
			"person.bob": {
				EntityID:    "person.bob",
				State:       "not_home",
				Attributes:  map[string]any{"friendly_name": "Bob"},
				LastChanged: now.Add(-3 * time.Hour),
			},
		},
	}

	tracker := NewTracker([]string{"person.alice", "person.bob"}, "America/Chicago", nil)
	_ = tracker.Initialize(context.Background(), getter)

	result, err := tracker.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}

	// Verify heading.
	if !strings.HasPrefix(result, "### People & Presence\n") {
		t.Errorf("expected heading, got:\n%s", result)
	}

	// Verify nested format with ISO 8601.
	if !strings.Contains(result, "- **Alice**:\n  - Home since 2026-02-15T16:30:00-06:00") {
		t.Errorf("expected nested format with RFC3339, got:\n%s", result)
	}

	// Verify Alice comes before Bob (insertion order).
	aliceIdx := strings.Index(result, "Alice")
	bobIdx := strings.Index(result, "Bob")
	if aliceIdx < 0 || bobIdx < 0 {
		t.Fatalf("expected both Alice and Bob in output:\n%s", result)
	}
	if aliceIdx > bobIdx {
		t.Errorf("expected Alice before Bob (insertion order), got:\n%s", result)
	}
}

func TestTracker_GetContext_WithRoom(t *testing.T) {
	now := time.Date(2026, 2, 15, 16, 30, 0, 0, time.UTC)
	getter := &mockStateGetter{
		states: map[string]*homeassistant.State{
			"person.alice": {
				EntityID:    "person.alice",
				State:       "home",
				Attributes:  map[string]any{"friendly_name": "Alice"},
				LastChanged: now,
			},
		},
	}

	tracker := NewTracker([]string{"person.alice"}, "UTC", nil)
	_ = tracker.Initialize(context.Background(), getter)
	tracker.UpdateRoom("person.alice", "office", "ap-hor-office")

	result, _ := tracker.GetContext(context.Background(), "")

	// Verify room appears as sub-bullet.
	if !strings.Contains(result, "  - Room: office\n") {
		t.Errorf("expected Room: office sub-bullet, got:\n%s", result)
	}
}

func TestTracker_GetContext_WithoutRoom(t *testing.T) {
	now := time.Date(2026, 2, 15, 16, 30, 0, 0, time.UTC)
	getter := &mockStateGetter{
		states: map[string]*homeassistant.State{
			"person.alice": {
				EntityID:    "person.alice",
				State:       "home",
				Attributes:  map[string]any{"friendly_name": "Alice"},
				LastChanged: now,
			},
		},
	}

	tracker := NewTracker([]string{"person.alice"}, "UTC", nil)
	_ = tracker.Initialize(context.Background(), getter)

	result, _ := tracker.GetContext(context.Background(), "")

	// No Room line should appear when Room is empty.
	if strings.Contains(result, "Room:") {
		t.Errorf("expected no Room line for empty room, got:\n%s", result)
	}
}

func TestTracker_GetContext_Empty(t *testing.T) {
	tracker := NewTracker(nil, "UTC", nil)

	result, err := tracker.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result for no entities, got %q", result)
	}
}

func TestTracker_GetContext_NotHome(t *testing.T) {
	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	getter := &mockStateGetter{
		states: map[string]*homeassistant.State{
			"person.alice": {
				EntityID:    "person.alice",
				State:       "not_home",
				Attributes:  map[string]any{"friendly_name": "Alice"},
				LastChanged: now,
			},
		},
	}

	tracker := NewTracker([]string{"person.alice"}, "UTC", nil)
	_ = tracker.Initialize(context.Background(), getter)

	result, _ := tracker.GetContext(context.Background(), "")
	if !strings.Contains(result, "Away since") {
		t.Errorf("expected not_home displayed as Away since, got:\n%s", result)
	}
	if strings.Contains(result, "not_home") {
		t.Errorf("raw not_home should not appear in output:\n%s", result)
	}
}

func TestTracker_UpdateRoom(t *testing.T) {
	tracker := NewTracker([]string{"person.alice"}, "UTC", nil)

	tracker.UpdateRoom("person.alice", "office", "ap-hor-office")

	tracker.mu.RLock()
	p := tracker.people["person.alice"]
	room := p.Room
	source := p.RoomSource
	sinceZero := p.RoomSince.IsZero()
	tracker.mu.RUnlock()

	if room != "office" {
		t.Errorf("expected room office, got %q", room)
	}
	if source != "ap-hor-office" {
		t.Errorf("expected source ap-hor-office, got %q", source)
	}
	if sinceZero {
		t.Error("expected RoomSince to be set")
	}
}

func TestTracker_UpdateRoom_SameRoom(t *testing.T) {
	tracker := NewTracker([]string{"person.alice"}, "UTC", nil)

	tracker.UpdateRoom("person.alice", "office", "ap-hor-office")

	tracker.mu.RLock()
	firstSince := tracker.people["person.alice"].RoomSince
	tracker.mu.RUnlock()

	// Same room — should not update RoomSince.
	time.Sleep(time.Millisecond)
	tracker.UpdateRoom("person.alice", "office", "ap-hor-office")

	tracker.mu.RLock()
	secondSince := tracker.people["person.alice"].RoomSince
	tracker.mu.RUnlock()

	if !firstSince.Equal(secondSince) {
		t.Errorf("expected RoomSince unchanged, got %v then %v", firstSince, secondSince)
	}
}

func TestTracker_UpdateRoom_ClearsRoom(t *testing.T) {
	tracker := NewTracker([]string{"person.alice"}, "UTC", nil)

	tracker.UpdateRoom("person.alice", "office", "ap-hor-office")
	tracker.UpdateRoom("person.alice", "", "")

	tracker.mu.RLock()
	p := tracker.people["person.alice"]
	room := p.Room
	sinceZero := p.RoomSince.IsZero()
	tracker.mu.RUnlock()

	if room != "" {
		t.Errorf("expected empty room, got %q", room)
	}
	if !sinceZero {
		t.Error("expected RoomSince to be zero after clearing")
	}
}

func TestTracker_UpdateRoom_IgnoresUntracked(t *testing.T) {
	tracker := NewTracker([]string{"person.alice"}, "UTC", nil)

	// Should not panic.
	tracker.UpdateRoom("person.unknown", "office", "ap-hor-office")
}

func TestTracker_SetDeviceMACs(t *testing.T) {
	tracker := NewTracker([]string{"person.alice"}, "UTC", nil)

	macs := []string{"aa:bb:cc:dd:ee:ff", "11:22:33:44:55:66"}
	tracker.SetDeviceMACs("person.alice", macs)

	tracker.mu.RLock()
	p := tracker.people["person.alice"]
	got := p.DeviceMACs
	tracker.mu.RUnlock()

	if len(got) != 2 {
		t.Fatalf("expected 2 MACs, got %d", len(got))
	}
	if got[0] != "aa:bb:cc:dd:ee:ff" || got[1] != "11:22:33:44:55:66" {
		t.Errorf("unexpected MACs: %v", got)
	}
}

func TestTracker_SetDeviceMACs_IgnoresUntracked(t *testing.T) {
	tracker := NewTracker([]string{"person.alice"}, "UTC", nil)

	// Should not panic.
	tracker.SetDeviceMACs("person.unknown", []string{"aa:bb:cc:dd:ee:ff"})
}

func TestTracker_ConcurrentAccess(t *testing.T) {
	now := time.Date(2026, 2, 15, 16, 30, 0, 0, time.UTC)
	getter := &mockStateGetter{
		states: map[string]*homeassistant.State{
			"person.alice": {
				EntityID:    "person.alice",
				State:       "home",
				Attributes:  map[string]any{"friendly_name": "Alice"},
				LastChanged: now,
			},
		},
	}

	tracker := NewTracker([]string{"person.alice"}, "UTC", nil)
	_ = tracker.Initialize(context.Background(), getter)

	const goroutines = 20
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	// Concurrent state changes.
	for range goroutines {
		go func() {
			defer wg.Done()
			for i := range iterations {
				state := "home"
				if i%2 == 0 {
					state = "not_home"
				}
				tracker.HandleStateChange("person.alice", "", state)
			}
		}()
	}

	// Concurrent context reads.
	for range goroutines {
		go func() {
			defer wg.Done()
			for range iterations {
				_, _ = tracker.GetContext(context.Background(), "")
			}
		}()
	}

	// Concurrent room updates.
	for range goroutines {
		go func() {
			defer wg.Done()
			for i := range iterations {
				room := "office"
				if i%2 == 0 {
					room = "bedroom"
				}
				tracker.UpdateRoom("person.alice", room, "ap-test")
			}
		}()
	}

	wg.Wait()
}

func TestFormatState(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"not_home", "Away"},
		{"NOT_HOME", "Away"},
		{"home", "Home"},
		{"zone.work", "Zone.work"},
		{"Unknown", "Unknown"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := formatState(tt.input)
			if got != tt.want {
				t.Errorf("formatState(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTitleCase(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"home", "Home"},
		{"away", "Away"},
		{"", ""},
		{"Home", "Home"},
		{"a", "A"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := titleCase(tt.input)
			if got != tt.want {
				t.Errorf("titleCase(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFriendlyNameFromEntityID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"person.nugget", "nugget"},
		{"person.alice_smith", "alice smith"},
		{"light.kitchen", "kitchen"},
		{"nodomain", "nodomain"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := friendlyNameFromEntityID(tt.input)
			if got != tt.want {
				t.Errorf("friendlyNameFromEntityID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
