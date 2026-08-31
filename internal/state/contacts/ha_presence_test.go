package contacts

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

type registryPresenceGetter struct {
	states   map[string]*homeassistant.State
	registry []homeassistant.EntityRegistryEntry
	gets     []string
}

type retryRegistryGetter struct {
	calls int
}

func (g *retryRegistryGetter) GetState(context.Context, string) (*homeassistant.State, error) {
	return nil, fmt.Errorf("not used")
}

func (g *retryRegistryGetter) GetEntityRegistry(context.Context) ([]homeassistant.EntityRegistryEntry, error) {
	g.calls++
	if g.calls == 1 {
		return nil, fmt.Errorf("websocket not connected")
	}
	return []homeassistant.EntityRegistryEntry{{EntityID: "device_tracker.phone", Platform: "bermuda"}}, nil
}

func (g *registryPresenceGetter) GetState(_ context.Context, entityID string) (*homeassistant.State, error) {
	g.gets = append(g.gets, entityID)
	state, ok := g.states[entityID]
	if !ok {
		return nil, fmt.Errorf("state %q not found", entityID)
	}
	return state, nil
}

func (g *registryPresenceGetter) GetEntityRegistry(context.Context) ([]homeassistant.EntityRegistryEntry, error) {
	return g.registry, nil
}

func TestPresenceTrackerInitializesProductionBermudaShape(t *testing.T) {
	personChanged := time.Date(2026, 8, 29, 18, 32, 38, 0, time.UTC)
	phoneUpdated := time.Date(2026, 8, 30, 23, 3, 19, 0, time.UTC)
	watchUpdated := time.Date(2026, 8, 30, 23, 38, 29, 0, time.UTC)
	const (
		personID = "person.alice"
		gpsID    = "device_tracker.alice_phone"
		phoneID  = "device_tracker.alice_phone_bermuda_tracker"
		watchID  = "device_tracker.alice_watch_bermuda_tracker"
	)
	getter := &registryPresenceGetter{
		states: map[string]*homeassistant.State{
			personID: {
				EntityID:    personID,
				State:       "home",
				LastChanged: personChanged,
				Attributes: map[string]any{
					"friendly_name":   "Alice",
					"source":          watchID,
					"device_trackers": []any{gpsID, phoneID, watchID},
				},
			},
			phoneID: bermudaTrackerState(phoneID, "home", "Office", "Desk Presence", phoneUpdated),
			watchID: bermudaTrackerState(watchID, "home", "Office", "Desk Presence", watchUpdated),
		},
		registry: []homeassistant.EntityRegistryEntry{
			{EntityID: gpsID, Platform: "mobile_app"},
			{EntityID: phoneID, Platform: "bermuda"},
			{EntityID: watchID, Platform: "bermuda"},
		},
	}
	tracker := NewPresenceTracker([]string{personID}, "UTC", nil)
	var ingestSets [][]string
	tracker.OnIngestEntitiesChange(func(entityIDs []string) {
		ingestSets = append(ingestSets, append([]string(nil), entityIDs...))
	})

	if err := tracker.Initialize(context.Background(), getter); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(getter.gets, []string{personID, phoneID, watchID}) {
		t.Fatalf("GetState calls = %v, want person plus registry-verified Bermuda trackers", getter.gets)
	}
	wantIngest := []string{personID, gpsID, phoneID, watchID}
	if !slices.Equal(tracker.IngestEntityIDs(), wantIngest) {
		t.Fatalf("ingest entities = %v, want %v", tracker.IngestEntityIDs(), wantIngest)
	}
	if len(ingestSets) != 2 || !slices.Equal(ingestSets[0], []string{personID}) || !slices.Equal(ingestSets[1], wantIngest) {
		t.Fatalf("ingest callbacks = %v", ingestSets)
	}

	snapshot, ok := tracker.Snapshot(personID)
	if !ok {
		t.Fatal("person snapshot missing")
	}
	if snapshot.State != "home" || !snapshot.Since.Equal(personChanged) {
		t.Errorf("person state = %+v", snapshot)
	}
	if snapshot.Room != "Office" || snapshot.RoomProvider != BermudaRoomProvider || snapshot.RoomSource != "Desk Presence" || snapshot.RoomConflict {
		t.Fatalf("resolved Bermuda room = %+v", snapshot)
	}
	if len(snapshot.RoomObservations) != 2 {
		t.Fatalf("room observations = %+v", snapshot.RoomObservations)
	}
	if snapshot.RoomObservations[0].Source != phoneID || snapshot.RoomObservations[0].Via != "Desk Presence" || !snapshot.RoomObservations[0].ObservedAt.Equal(phoneUpdated) {
		t.Errorf("phone observation = %+v", snapshot.RoomObservations[0])
	}
	if snapshot.RoomObservations[1].Source != watchID || !snapshot.RoomObservations[1].ObservedAt.Equal(watchUpdated) {
		t.Errorf("watch observation = %+v", snapshot.RoomObservations[1])
	}
}

func TestPresenceTrackerHandlesAttributeOnlyBermudaUpdatesAndUnlink(t *testing.T) {
	const (
		personID = "person.alice"
		phoneID  = "device_tracker.alice_phone_bermuda_tracker"
		watchID  = "device_tracker.alice_watch_bermuda_tracker"
	)
	getter := &registryPresenceGetter{
		states: map[string]*homeassistant.State{
			personID: {
				EntityID: personID, State: "home", LastChanged: time.Now().Add(-time.Hour),
				Attributes: map[string]any{"device_trackers": []any{phoneID, watchID}},
			},
			phoneID: bermudaTrackerState(phoneID, "home", "Office", "Desk Presence", time.Now().Add(-time.Minute)),
			watchID: bermudaTrackerState(watchID, "home", "Office", "Desk Presence", time.Now().Add(-time.Minute)),
		},
		registry: []homeassistant.EntityRegistryEntry{
			{EntityID: phoneID, Platform: "bermuda"},
			{EntityID: watchID, Platform: "bermuda"},
		},
	}
	tracker := NewPresenceTracker([]string{personID}, "UTC", nil)
	if err := tracker.Initialize(context.Background(), getter); err != nil {
		t.Fatal(err)
	}

	watchUpdated := time.Date(2026, 8, 30, 23, 45, 0, 0, time.UTC)
	tracker.HandleHAStateChange(homeassistant.StateChangedData{
		EntityID: watchID,
		OldState: bermudaTrackerState(watchID, "home", "Office", "Desk Presence", watchUpdated.Add(-time.Minute)),
		NewState: bermudaTrackerState(watchID, "home", "Kitchen", "Kitchen Proxy", watchUpdated),
	})
	conflicted, _ := tracker.Snapshot(personID)
	if !conflicted.RoomConflict || conflicted.Room != "" {
		t.Fatalf("attribute-only disagreement = %+v", conflicted)
	}

	phoneUpdated := watchUpdated.Add(time.Second)
	tracker.HandleHAStateChange(homeassistant.StateChangedData{
		EntityID: phoneID,
		OldState: bermudaTrackerState(phoneID, "home", "Office", "Desk Presence", phoneUpdated.Add(-time.Minute)),
		NewState: bermudaTrackerState(phoneID, "home", "Kitchen", "Kitchen Proxy", phoneUpdated),
	})
	resolved, _ := tracker.Snapshot(personID)
	if resolved.RoomConflict || resolved.Room != "Kitchen" || resolved.RoomProvider != BermudaRoomProvider || resolved.RoomSource != "Kitchen Proxy" {
		t.Fatalf("attribute-only consensus = %+v", resolved)
	}

	tracker.HandleHAStateChange(homeassistant.StateChangedData{
		EntityID: personID,
		OldState: &homeassistant.State{EntityID: personID, State: "home"},
		NewState: &homeassistant.State{
			EntityID: personID,
			State:    "home",
			Attributes: map[string]any{
				"device_trackers": []any{phoneID},
			},
		},
	})
	unlinked, _ := tracker.Snapshot(personID)
	if len(unlinked.RoomObservations) != 1 || unlinked.RoomObservations[0].Source != phoneID || unlinked.Room != "Kitchen" {
		t.Fatalf("unlink retained stale watch evidence: %+v", unlinked)
	}
	if !slices.Equal(tracker.IngestEntityIDs(), []string{personID, phoneID}) {
		t.Fatalf("ingest entities after unlink = %v", tracker.IngestEntityIDs())
	}

	tracker.HandleHAStateChange(homeassistant.StateChangedData{
		EntityID: phoneID,
		OldState: bermudaTrackerState(phoneID, "home", "Kitchen", "Kitchen Proxy", phoneUpdated),
		NewState: bermudaTrackerState(phoneID, "not_home", "", "", phoneUpdated.Add(time.Second)),
	})
	withdrawn, _ := tracker.Snapshot(personID)
	if withdrawn.Room != "" || len(withdrawn.RoomObservations) != 0 || withdrawn.RoomConflict {
		t.Fatalf("not_home tracker retained room evidence: %+v", withdrawn)
	}
}

func TestLinkedDeviceTrackerIDs(t *testing.T) {
	tooMany := make([]any, maxLinkedDeviceTrackersPerPerson+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("device_tracker.tracker_%d", i)
	}
	tests := []struct {
		name    string
		attrs   map[string]any
		want    []string
		wantErr bool
	}{
		{name: "missing", attrs: nil},
		{
			name: "sorted deduplicated trackers only",
			attrs: map[string]any{"device_trackers": []any{
				"device_tracker.watch", "sensor.not_a_tracker", " device_tracker.phone ", "device_tracker.watch",
			}},
			want: []string{"device_tracker.phone", "device_tracker.watch"},
		},
		{name: "wrong container", attrs: map[string]any{"device_trackers": "device_tracker.phone"}, wantErr: true},
		{name: "non-string member", attrs: map[string]any{"device_trackers": []any{42}}, wantErr: true},
		{name: "bounded", attrs: map[string]any{"device_trackers": tooMany}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := linkedDeviceTrackerIDs(tt.attrs)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("trackers = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetEntityRegistryWithRetryAllowsWebSocketStartup(t *testing.T) {
	getter := &retryRegistryGetter{}
	entries, err := getEntityRegistryWithRetry(context.Background(), getter, []time.Duration{0})
	if err != nil {
		t.Fatal(err)
	}
	if getter.calls != 2 || len(entries) != 1 || entries[0].Platform != "bermuda" {
		t.Fatalf("calls = %d, entries = %+v", getter.calls, entries)
	}
}

func bermudaTrackerState(entityID, state, area, scanner string, updatedAt time.Time) *homeassistant.State {
	return &homeassistant.State{
		EntityID:    entityID,
		State:       state,
		LastChanged: updatedAt,
		LastUpdated: updatedAt,
		Attributes: map[string]any{
			"area":        area,
			"scanner":     scanner,
			"source_type": "bluetooth_le",
		},
	}
}
