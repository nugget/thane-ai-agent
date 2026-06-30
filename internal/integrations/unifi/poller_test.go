package unifi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockLocator struct {
	mu        sync.Mutex
	locations []DeviceLocation
	err       error
}

func (m *mockLocator) LocateDevices(_ context.Context) ([]DeviceLocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	cp := make([]DeviceLocation, len(m.locations))
	copy(cp, m.locations)
	return cp, nil
}

func (m *mockLocator) Ping(_ context.Context) error {
	return m.err
}

func (m *mockLocator) setLocations(locs []DeviceLocation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.locations = locs
}

func (m *mockLocator) setErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

type roomUpdate struct {
	entityID string
	room     string
	source   string
}

type mockUpdater struct {
	mu      sync.Mutex
	updates []roomUpdate
}

func (m *mockUpdater) UpdateRoom(entityID, room, source string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updates = append(m.updates, roomUpdate{entityID, room, source})
}

func (m *mockUpdater) getUpdates() []roomUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]roomUpdate, len(m.updates))
	copy(cp, m.updates)
	return cp
}

// mustPoll calls Poll and fails the test if an unexpected error occurs.
func mustPoll(t *testing.T, p *Poller) {
	t.Helper()
	if err := p.Poll(context.Background()); err != nil {
		t.Fatalf("unexpected Poll error: %v", err)
	}
}

func TestPoller_BasicRoomUpdate(t *testing.T) {
	locator := &mockLocator{
		locations: []DeviceLocation{
			{MAC: "aa:bb:cc:dd:ee:ff", APName: "ap-office", Signal: -45, LastSeen: 1000},
		},
	}
	updater := &mockUpdater{}

	p := NewPoller(PollerConfig{
		Locator:      locator,
		Updater:      updater,
		PollInterval: time.Hour, // won't tick in test
		DeviceOwners: map[string]string{"aa:bb:cc:dd:ee:ff": "person.alice"},
		APRooms:      map[string]string{"ap-office": "office"},
	})

	// First poll — starts debounce, no update yet.
	mustPoll(t, p)
	updates := updater.getUpdates()
	if len(updates) != 0 {
		t.Fatalf("expected 0 updates after first poll, got %d", len(updates))
	}

	// Second poll — debounce threshold met, should update.
	mustPoll(t, p)
	updates = updater.getUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 update after second poll, got %d", len(updates))
	}
	if updates[0].entityID != "person.alice" {
		t.Errorf("expected entity person.alice, got %q", updates[0].entityID)
	}
	if updates[0].room != "office" {
		t.Errorf("expected room office, got %q", updates[0].room)
	}
	if updates[0].source != "ap-office" {
		t.Errorf("expected source ap-office, got %q", updates[0].source)
	}
}

func TestPoller_DebouncePreventsSinglePoll(t *testing.T) {
	locator := &mockLocator{
		locations: []DeviceLocation{
			{MAC: "aa:bb:cc:dd:ee:ff", APName: "ap-office", Signal: -45, LastSeen: 1000},
		},
	}
	updater := &mockUpdater{}

	p := NewPoller(PollerConfig{
		Locator:      locator,
		Updater:      updater,
		PollInterval: time.Hour,
		DeviceOwners: map[string]string{"aa:bb:cc:dd:ee:ff": "person.alice"},
		APRooms:      map[string]string{"ap-office": "office"},
	})

	// Single poll should not update.
	mustPoll(t, p)

	updates := updater.getUpdates()
	if len(updates) != 0 {
		t.Errorf("expected no updates after single poll, got %d", len(updates))
	}
}

func TestPoller_RoomChangeResetsDebounce(t *testing.T) {
	locator := &mockLocator{
		locations: []DeviceLocation{
			{MAC: "aa:bb:cc:dd:ee:ff", APName: "ap-office", Signal: -45, LastSeen: 1000},
		},
	}
	updater := &mockUpdater{}

	p := NewPoller(PollerConfig{
		Locator:      locator,
		Updater:      updater,
		PollInterval: time.Hour,
		DeviceOwners: map[string]string{"aa:bb:cc:dd:ee:ff": "person.alice"},
		APRooms:      map[string]string{"ap-office": "office", "ap-bedroom": "bedroom"},
	})

	// First poll on office — starts debounce.
	mustPoll(t, p)

	// Switch to bedroom — resets debounce.
	locator.setLocations([]DeviceLocation{
		{MAC: "aa:bb:cc:dd:ee:ff", APName: "ap-bedroom", Signal: -50, LastSeen: 1001},
	})
	mustPoll(t, p)

	// Should have no updates yet (debounce reset).
	updates := updater.getUpdates()
	if len(updates) != 0 {
		t.Fatalf("expected no updates after room change, got %d", len(updates))
	}

	// Third poll on bedroom — debounce met.
	mustPoll(t, p)
	updates = updater.getUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	if updates[0].room != "bedroom" {
		t.Errorf("expected room bedroom, got %q", updates[0].room)
	}
}

func TestPoller_MultipleDevices(t *testing.T) {
	locator := &mockLocator{
		locations: []DeviceLocation{
			{MAC: "aa:bb:cc:dd:ee:ff", APName: "ap-office", Signal: -45, LastSeen: 900},   // older
			{MAC: "11:22:33:44:55:66", APName: "ap-bedroom", Signal: -50, LastSeen: 1000}, // newer
		},
	}
	updater := &mockUpdater{}

	p := NewPoller(PollerConfig{
		Locator: locator,
		Updater: updater,
		DeviceOwners: map[string]string{
			"aa:bb:cc:dd:ee:ff": "person.alice",
			"11:22:33:44:55:66": "person.alice", // same person, two devices
		},
		APRooms:      map[string]string{"ap-office": "office", "ap-bedroom": "bedroom"},
		PollInterval: time.Hour,
	})

	// Two polls to pass debounce.
	mustPoll(t, p)
	mustPoll(t, p)

	updates := updater.getUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	// Should use the most recently seen device (bedroom, LastSeen=1000).
	if updates[0].room != "bedroom" {
		t.Errorf("expected bedroom (most recent device), got %q", updates[0].room)
	}
}

func TestPoller_UnknownAP(t *testing.T) {
	locator := &mockLocator{
		locations: []DeviceLocation{
			{MAC: "aa:bb:cc:dd:ee:ff", APName: "ap-unknown", Signal: -45, LastSeen: 1000},
		},
	}
	updater := &mockUpdater{}

	p := NewPoller(PollerConfig{
		Locator:      locator,
		Updater:      updater,
		PollInterval: time.Hour,
		DeviceOwners: map[string]string{"aa:bb:cc:dd:ee:ff": "person.alice"},
		APRooms:      map[string]string{"ap-office": "office"}, // ap-unknown not listed
	})

	mustPoll(t, p)
	mustPoll(t, p)

	updates := updater.getUpdates()
	if len(updates) != 0 {
		t.Errorf("expected no updates for unknown AP, got %d", len(updates))
	}
}

// TestPoller_ToleratesTransientFailures verifies a flaky gateway doesn't alarm
// on the first failures: Poll returns nil (no loop error) until FailureThreshold
// consecutive failures, at which point it surfaces the error.
func TestPoller_ToleratesTransientFailures(t *testing.T) {
	locator := &mockLocator{err: fmt.Errorf("UniFi API error 500: internal error")}
	updater := &mockUpdater{}

	p := NewPoller(PollerConfig{
		Locator:      locator,
		Updater:      updater,
		PollInterval: time.Hour,
		DeviceOwners: map[string]string{"aa:bb:cc:dd:ee:ff": "person.alice"},
		APRooms:      map[string]string{"ap-office": "office"},
	}) // default threshold = 3

	// First two failures are tolerated (no alarm).
	for i := 1; i <= defaultFailureThreshold-1; i++ {
		if err := p.Poll(context.Background()); err != nil {
			t.Fatalf("poll %d: expected tolerated (nil) error, got %v", i, err)
		}
	}
	// The threshold'th consecutive failure surfaces the error (alarm).
	err := p.Poll(context.Background())
	if err == nil {
		t.Fatal("expected error once failure threshold reached")
	}
	if !strings.Contains(err.Error(), "consecutive polls") {
		t.Errorf("alarm error = %q, want it to mention consecutive polls", err)
	}

	if updates := updater.getUpdates(); len(updates) != 0 {
		t.Errorf("expected no room updates on failure, got %d", len(updates))
	}
}

// TestPoller_SuccessResetsFailureStreak verifies a single good poll clears the
// streak so the budget resets — a gateway that recovers between blips never alarms.
func TestPoller_SuccessResetsFailureStreak(t *testing.T) {
	locator := &mockLocator{err: fmt.Errorf("UniFi API error 500: internal error")}
	updater := &mockUpdater{}

	p := NewPoller(PollerConfig{
		Locator:      locator,
		Updater:      updater,
		PollInterval: time.Hour,
		DeviceOwners: map[string]string{"aa:bb:cc:dd:ee:ff": "person.alice"},
		APRooms:      map[string]string{"ap-office": "office"},
	})

	// Two failures (tolerated), then a success resets the streak.
	mustPoll(t, p)
	mustPoll(t, p)
	locator.setErr(nil)
	locator.setLocations([]DeviceLocation{{MAC: "aa:bb:cc:dd:ee:ff", APName: "ap-office", LastSeen: 1000}})
	mustPoll(t, p) // success — streak reset to 0

	// Failures resume; the budget is full again, so the next two are tolerated.
	locator.setErr(fmt.Errorf("UniFi API error 500: internal error"))
	mustPoll(t, p)
	mustPoll(t, p)
	if err := p.Poll(context.Background()); err == nil {
		t.Fatal("expected error only after threshold failures following the reset")
	}
}

// TestPoller_FailureThresholdConfigurable verifies the budget honors the config.
func TestPoller_FailureThresholdConfigurable(t *testing.T) {
	locator := &mockLocator{err: fmt.Errorf("UniFi API error 503")}
	p := NewPoller(PollerConfig{
		Locator:          locator,
		Updater:          &mockUpdater{},
		PollInterval:     time.Hour,
		FailureThreshold: 1, // alarm on the very first failure
	})

	if err := p.Poll(context.Background()); err == nil {
		t.Fatal("expected error on first failure with threshold=1")
	}
}

func TestPoller_ClearsPendingWhenDeviceGone(t *testing.T) {
	locator := &mockLocator{
		locations: []DeviceLocation{
			{MAC: "aa:bb:cc:dd:ee:ff", APName: "ap-office", Signal: -45, LastSeen: 1000},
		},
	}
	updater := &mockUpdater{}

	p := NewPoller(PollerConfig{
		Locator:      locator,
		Updater:      updater,
		PollInterval: time.Hour,
		DeviceOwners: map[string]string{"aa:bb:cc:dd:ee:ff": "person.alice"},
		APRooms:      map[string]string{"ap-office": "office"},
	})

	// First poll — starts debounce.
	mustPoll(t, p)

	p.mu.Lock()
	hasPending := len(p.pending) > 0
	p.mu.Unlock()
	if !hasPending {
		t.Fatal("expected pending entry after first poll")
	}

	// Device disappears.
	locator.setLocations(nil)
	mustPoll(t, p)

	p.mu.Lock()
	hasPending = len(p.pending) > 0
	p.mu.Unlock()
	if hasPending {
		t.Error("expected pending entry cleared when device gone")
	}
}
