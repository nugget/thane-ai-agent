package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

type whereaboutsFixture struct {
	deps      CounterpartyToolDeps
	contactID string
}

type staticWhereaboutsStateGetter map[string]*homeassistant.State

func (g staticWhereaboutsStateGetter) GetState(_ context.Context, entityID string) (*homeassistant.State, error) {
	state, ok := g[entityID]
	if !ok {
		return nil, fmt.Errorf("state %q not found", entityID)
	}
	return state, nil
}

func (g staticWhereaboutsStateGetter) GetEntityRegistry(context.Context) ([]homeassistant.EntityRegistryEntry, error) {
	return nil, nil
}

func (g staticWhereaboutsStateGetter) InvalidateRegistryCache() {}

type registryWhereaboutsStateGetter struct {
	states   map[string]*homeassistant.State
	registry []homeassistant.EntityRegistryEntry
}

func (g registryWhereaboutsStateGetter) GetState(_ context.Context, entityID string) (*homeassistant.State, error) {
	state, ok := g.states[entityID]
	if !ok {
		return nil, fmt.Errorf("state %q not found", entityID)
	}
	return state, nil
}

func (g registryWhereaboutsStateGetter) GetEntityRegistry(context.Context) ([]homeassistant.EntityRegistryEntry, error) {
	return g.registry, nil
}

func (g registryWhereaboutsStateGetter) InvalidateRegistryCache() {}

// newWhereaboutsFixture builds a contact ("Alice Operator") with an HA
// person binding, one bound companion account carrying the given
// observations, and a fake presence snapshot.
func newWhereaboutsFixture(t *testing.T, state, room string, observations map[string]companion.ObservationStatus) whereaboutsFixture {
	t.Helper()
	cdb, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("contacts db: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })
	contactStore, err := contacts.NewStore(cdb, nil)
	if err != nil {
		t.Fatalf("contacts store: %v", err)
	}
	alice, err := contactStore.Upsert(&contacts.Contact{FormattedName: "Alice Operator"})
	if err != nil {
		t.Fatal(err)
	}
	if err := contactStore.SetHAPersonEntity(alice.ID, "person.alice"); err != nil {
		t.Fatal(err)
	}

	companionStore := newObservationToolStore(t)
	now := time.Now().UTC()
	i := 0
	for device, status := range observations {
		seedLocation(t, companionStore, "alice-acct", device, status, now.Add(-time.Duration(30+i*30)*time.Minute))
		i++
	}

	deps := CounterpartyToolDeps{
		Contacts:   contactStore,
		Companions: companionStore,
		Presence: func(entity string) (contacts.PersonSnapshot, bool) {
			if entity != "person.alice" {
				return contacts.PersonSnapshot{}, false
			}
			snap := contacts.PersonSnapshot{EntityID: entity, State: state, Since: now.Add(-2 * time.Hour)}
			if room != "" {
				snap.Room = room
				snap.RoomProvider = "unifi"
				snap.RoomSource = "ap-office"
				snap.RoomSince = now.Add(-20 * time.Minute)
			}
			return snap, true
		},
		AccountsForContact: func(id string) []string {
			if id == alice.ID.String() {
				return []string{"alice-acct"}
			}
			return nil
		},
		LiveIdentities: func() map[[2]string]bool { return nil },
	}
	return whereaboutsFixture{deps: deps, contactID: alice.ID.String()}
}

func decodeWhereabouts(t *testing.T, out string) whereaboutsResult {
	t.Helper()
	var res whereaboutsResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	return res
}

func TestContactWhereaboutsHomeRanking(t *testing.T) {
	fx := newWhereaboutsFixture(t, "home", "office", map[string]companion.ObservationStatus{"device-1": companion.ObservationAvailable})
	out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := decodeWhereabouts(t, out)
	if res.Contact != "Alice Operator" || res.ContactID != fx.contactID {
		t.Errorf("identity = %q/%q", res.Contact, res.ContactID)
	}
	if len(res.Sources) != 3 {
		t.Fatalf("sources = %+v, want room + zone + device", res.Sources)
	}
	if res.Sources[0].Source != "unifi_room" || res.Sources[0].Room != "office" || res.Sources[0].RoomVia != "ap-office" {
		t.Errorf("home ranking: leading source = %+v, want provider-attributed UniFi room", res.Sources[0])
	}
	if res.Sources[1].Source != "ha_person_zone" || res.Sources[2].Source != "companion_location" {
		t.Errorf("home order = %s, %s", res.Sources[1].Source, res.Sources[2].Source)
	}
	if res.BestSource != "unifi_room" || !strings.Contains(res.Basis, "home") {
		t.Errorf("verdict = %q / %q", res.BestSource, res.Basis)
	}
}

func TestContactWhereaboutsReportsRoomConflictWithoutGuessing(t *testing.T) {
	fx := newWhereaboutsFixture(t, "home", "", nil)
	now := time.Now().UTC()
	fx.deps.Presence = func(entity string) (contacts.PersonSnapshot, bool) {
		if entity != "person.alice" {
			return contacts.PersonSnapshot{}, false
		}
		return contacts.PersonSnapshot{
			EntityID:     entity,
			State:        "home",
			Since:        now.Add(-2 * time.Hour),
			Room:         "office",
			RoomProvider: "bermuda",
			RoomSource:   "device_tracker.phone_bermuda",
			RoomConflict: true,
			RoomObservations: []contacts.RoomObservation{
				{Room: "office", Provider: "bermuda", Source: "device_tracker.phone_bermuda", ObservedAt: now.Add(-time.Minute)},
				{Room: "kitchen", Provider: "unifi", Source: "ap-kitchen", ObservedAt: now.Add(-2 * time.Minute)},
			},
		}, true
	}

	out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
	if err != nil {
		t.Fatal(err)
	}
	res := decodeWhereabouts(t, out)
	if !res.RoomConflict {
		t.Fatalf("result = %s, want room_conflict", out)
	}
	if len(res.Sources) != 1 || res.Sources[0].Source != "ha_person_zone" || res.Sources[0].Room != "" {
		t.Fatalf("sources = %+v, want only the home zone floor", res.Sources)
	}
	if res.BestSource != "ha_person_zone" || !strings.Contains(res.Basis, "conflict") || !strings.Contains(res.Basis, "no room is asserted") {
		t.Errorf("verdict = %q / %q", res.BestSource, res.Basis)
	}
}

// TestContactWhereaboutsProductionPersonShapePreservesRoomProvider mirrors the
// material parts of a production HA person backed by regular and Bermuda
// device trackers. The aggregate person's active source must not overwrite the
// explicit provider that actually supplied Thane's room observation.
func TestContactWhereaboutsProductionPersonShapePreservesRoomProvider(t *testing.T) {
	now := time.Now().UTC()
	getter := staticWhereaboutsStateGetter{
		"person.alice": {
			EntityID: "person.alice",
			State:    "home",
			Attributes: map[string]any{
				"friendly_name": "Alice",
				"source":        "device_tracker.alice_phone_bermuda_tracker",
				"device_trackers": []any{
					"device_tracker.alice_phone",
					"device_tracker.alice_phone_bermuda_tracker",
				},
			},
			LastChanged: now.Add(-24 * time.Hour),
			LastUpdated: now,
		},
	}
	tracker := contacts.NewPresenceTracker([]string{"person.alice"}, "UTC", nil)
	if err := tracker.Initialize(context.Background(), getter); err != nil {
		t.Fatal(err)
	}
	tracker.UpdateRoom("person.alice", "office", "unifi", "ap-office")

	fx := newWhereaboutsFixture(t, "home", "", nil)
	fx.deps.Presence = tracker.Snapshot
	out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
	if err != nil {
		t.Fatal(err)
	}
	res := decodeWhereabouts(t, out)
	if len(res.Sources) < 2 {
		t.Fatalf("sources = %+v, want room and zone", res.Sources)
	}
	room := res.Sources[0]
	if room.Source != "unifi_room" || room.RoomVia != "ap-office" {
		t.Fatalf("room source = %+v, want explicit UniFi provenance", room)
	}
	if strings.Contains(out, "bermuda_room") {
		t.Fatalf("aggregate HA source fabricated Bermuda room provenance: %s", out)
	}
}

func TestContactWhereaboutsProductionBermudaConsensusUsesScannerEvidence(t *testing.T) {
	now := time.Now().UTC()
	const (
		phoneID = "device_tracker.alice_phone_bermuda_tracker"
		watchID = "device_tracker.alice_watch_bermuda_tracker"
	)
	getter := registryWhereaboutsStateGetter{
		states: map[string]*homeassistant.State{
			"person.alice": {
				EntityID: "person.alice", State: "home", LastChanged: now.Add(-2 * time.Hour),
				Attributes: map[string]any{
					"source":          watchID,
					"device_trackers": []any{phoneID, watchID},
				},
			},
			phoneID: {
				EntityID: phoneID, State: "home", LastUpdated: now.Add(-2 * time.Minute),
				Attributes: map[string]any{"area": "Office", "scanner": "Desk Presence"},
			},
			watchID: {
				EntityID: watchID, State: "home", LastUpdated: now.Add(-time.Minute),
				Attributes: map[string]any{"area": "Office", "scanner": "Desk Presence"},
			},
		},
		registry: []homeassistant.EntityRegistryEntry{
			{EntityID: phoneID, Platform: "bermuda"},
			{EntityID: watchID, Platform: "bermuda"},
		},
	}
	tracker := contacts.NewPresenceTracker([]string{"person.alice"}, "UTC", nil)
	if err := tracker.Initialize(context.Background(), getter); err != nil {
		t.Fatal(err)
	}

	fx := newWhereaboutsFixture(t, "home", "", nil)
	fx.deps.Presence = tracker.Snapshot
	out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
	if err != nil {
		t.Fatal(err)
	}
	res := decodeWhereabouts(t, out)
	if len(res.Sources) < 2 {
		t.Fatalf("sources = %+v, want Bermuda room and person zone", res.Sources)
	}
	room := res.Sources[0]
	if room.Source != "bermuda_room" || room.Room != "Office" || room.RoomVia != "Desk Presence" {
		t.Fatalf("Bermuda room source = %+v", room)
	}
	if strings.Contains(out, phoneID) || strings.Contains(out, watchID) {
		t.Fatalf("model-facing result leaked observation identity instead of scanner evidence: %s", out)
	}
}

func TestContactWhereaboutsBermudaWithoutScannerKeepsTrackerPrivate(t *testing.T) {
	now := time.Now().UTC()
	const trackerID = "device_tracker.alice_private_watch_bermuda_tracker"
	getter := registryWhereaboutsStateGetter{
		states: map[string]*homeassistant.State{
			"person.alice": {
				EntityID: "person.alice", State: "home", LastChanged: now.Add(-2 * time.Hour), LastUpdated: now.Add(-2 * time.Hour),
				Attributes: map[string]any{"device_trackers": []any{trackerID}},
			},
			trackerID: {
				EntityID: trackerID, State: "home", LastUpdated: now.Add(-time.Minute),
				Attributes: map[string]any{"area": "Office"},
			},
		},
		registry: []homeassistant.EntityRegistryEntry{{EntityID: trackerID, Platform: "bermuda"}},
	}
	tracker := contacts.NewPresenceTracker([]string{"person.alice"}, "UTC", nil)
	if err := tracker.Initialize(context.Background(), getter); err != nil {
		t.Fatal(err)
	}

	fx := newWhereaboutsFixture(t, "home", "", nil)
	fx.deps.Presence = tracker.Snapshot
	out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
	if err != nil {
		t.Fatal(err)
	}
	res := decodeWhereabouts(t, out)
	if len(res.Sources) < 2 || res.Sources[0].Source != "bermuda_room" || res.Sources[0].Room != "Office" || res.Sources[0].RoomVia != "" {
		t.Fatalf("Bermuda room without scanner = %+v", res.Sources)
	}
	if strings.Contains(out, trackerID) {
		t.Fatalf("model-facing result leaked private tracker identity: %s", out)
	}
}

func TestWhereaboutsRoomSource(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     string
	}{
		{name: "UniFi", provider: "unifi", want: "unifi_room"},
		{name: "Bermuda normalized", provider: " Bermuda ", want: "bermuda_room"},
		{name: "unknown", provider: "future-provider", want: "room_presence"},
		{name: "legacy empty", provider: "", want: "room_presence"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := whereaboutsRoomSource(tt.provider); got != tt.want {
				t.Errorf("whereaboutsRoomSource(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}

func TestContactWhereaboutsAwayRanking(t *testing.T) {
	fx := newWhereaboutsFixture(t, "not_home", "", map[string]companion.ObservationStatus{"device-1": companion.ObservationAvailable})
	out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := decodeWhereabouts(t, out)
	if res.Sources[0].Source != "companion_location" {
		t.Errorf("away ranking: leading source = %+v, want device location", res.Sources[0])
	}
	if res.Sources[0].Location == nil || !strings.Contains(string(res.Sources[0].Location), "latitude") {
		t.Errorf("device payload missing: %+v", res.Sources[0])
	}
	last := res.Sources[len(res.Sources)-1]
	if last.Source != "ha_person_zone" || last.State != "not_home" {
		t.Errorf("zone floor = %+v", last)
	}
	if res.BestSource != "companion_location" {
		t.Errorf("BestSource = %q", res.BestSource)
	}
}

func TestContactWhereaboutsWithdrawnSinks(t *testing.T) {
	fx := newWhereaboutsFixture(t, "not_home", "", map[string]companion.ObservationStatus{
		"device-1": companion.ObservationWithdrawn,
		"device-2": companion.ObservationAvailable,
	})
	out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := decodeWhereabouts(t, out)
	var deviceEntries []whereaboutsSource
	for _, src := range res.Sources {
		if src.Source == "companion_location" {
			deviceEntries = append(deviceEntries, src)
		}
	}
	if len(deviceEntries) != 2 {
		t.Fatalf("device entries = %+v", deviceEntries)
	}
	if deviceEntries[0].Status != "available" || deviceEntries[1].Status != "withdrawn" {
		t.Errorf("withdrawn did not sink: %+v", deviceEntries)
	}
	if deviceEntries[1].Location != nil {
		t.Errorf("withdrawn entry leaked payload: %+v", deviceEntries[1])
	}
}

func TestContactWhereaboutsNoSources(t *testing.T) {
	cdb, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cdb.Close() })
	store, err := contacts.NewStore(cdb, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert(&contacts.Contact{FormattedName: "Bob Guest"}); err != nil {
		t.Fatal(err)
	}
	deps := CounterpartyToolDeps{Contacts: store}
	_, err = handleContactWhereabouts(context.Background(), deps, "Bob Guest", "")
	if err == nil || !strings.Contains(err.Error(), "no whereabouts source") {
		t.Fatalf("unbound contact must explain the configuration gap: %v", err)
	}
}

func TestContactWhereaboutsResolution(t *testing.T) {
	fx := newWhereaboutsFixture(t, "home", "", nil)
	if _, err := handleContactWhereabouts(context.Background(), fx.deps, "", ""); err == nil {
		t.Error("empty selector accepted")
	}
	if _, err := handleContactWhereabouts(context.Background(), fx.deps, "Nobody Real", ""); err == nil || !strings.Contains(err.Error(), "Nobody Real") {
		t.Errorf("unknown name error must echo the selection: %v", err)
	}
	out, err := handleContactWhereabouts(context.Background(), fx.deps, "", fx.contactID)
	if err != nil {
		t.Fatalf("by-id: %v", err)
	}
	if res := decodeWhereabouts(t, out); res.BestSource == "" {
		t.Errorf("by-id result = %+v", res)
	}
}

// TestContactWhereaboutsWithdrawnTrailsZoneFloor pins the fixed
// ranking: withdrawn entries trail even the zone floor, so an
// all-withdrawn fleet can never make a payload-less device entry the
// best source.
func TestContactWhereaboutsWithdrawnTrailsZoneFloor(t *testing.T) {
	fx := newWhereaboutsFixture(t, "not_home", "", map[string]companion.ObservationStatus{"device-1": companion.ObservationWithdrawn})
	out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := decodeWhereabouts(t, out)
	if res.BestSource != "ha_person_zone" {
		t.Errorf("BestSource = %q, want the zone floor when every fix is withdrawn", res.BestSource)
	}
	if last := res.Sources[len(res.Sources)-1]; last.Source != "companion_location" || last.Status != "withdrawn" {
		t.Errorf("withdrawn entry does not trail: %+v", res.Sources)
	}
	if strings.Contains(res.Basis, "device location leads") || !strings.Contains(res.Basis, "zone state leads") {
		t.Errorf("basis contradicts the zone-floor ranking: %q", res.Basis)
	}
}

func TestContactWhereaboutsWithdrawnOnlyBasis(t *testing.T) {
	tests := []struct {
		name  string
		state string
	}{
		{name: "away", state: "not_home"},
		{name: "unknown", state: "Unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newWhereaboutsFixture(t, tt.state, "", map[string]companion.ObservationStatus{
				"device-1": companion.ObservationWithdrawn,
			})
			out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			res := decodeWhereabouts(t, out)
			if res.BestSource != "ha_person_zone" {
				t.Errorf("BestSource = %q, want zone state", res.BestSource)
			}
			if strings.Contains(res.Basis, "device location leads") || !strings.Contains(res.Basis, "zone state leads") {
				t.Errorf("basis contradicts withdrawn-only ranking: %q", res.Basis)
			}
		})
	}
}

func TestContactWhereaboutsDeviceCapPreservesZoneFloor(t *testing.T) {
	observations := make(map[string]companion.ObservationStatus, maxWhereaboutsSources+4)
	for i := 0; i < maxWhereaboutsSources+4; i++ {
		observations[fmt.Sprintf("device-%02d", i)] = companion.ObservationAvailable
	}
	fx := newWhereaboutsFixture(t, "not_home", "", observations)
	out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := decodeWhereabouts(t, out)
	if len(res.Sources) != maxWhereaboutsSources {
		t.Fatalf("sources = %d, want cap %d", len(res.Sources), maxWhereaboutsSources)
	}
	if res.TruncatedDevices != 5 {
		t.Errorf("TruncatedDevices = %d, want 5 device entries", res.TruncatedDevices)
	}
	deviceSources := 0
	zoneSources := 0
	for _, src := range res.Sources {
		switch src.Source {
		case "companion_location":
			deviceSources++
		case "ha_person_zone":
			zoneSources++
		}
	}
	if deviceSources != maxWhereaboutsSources-1 || zoneSources != 1 {
		t.Errorf("capped sources = %d devices/%d zones, want %d/1", deviceSources, zoneSources, maxWhereaboutsSources-1)
	}
	if last := res.Sources[len(res.Sources)-1]; last.Source != "ha_person_zone" {
		t.Errorf("zone floor was not preserved at the end: %+v", res.Sources)
	}
}

func TestContactWhereaboutsWithdrawnWithoutPresenceHasNoBestSource(t *testing.T) {
	fx := newWhereaboutsFixture(t, "not_home", "", map[string]companion.ObservationStatus{
		"device-1": companion.ObservationWithdrawn,
	})
	if err := fx.deps.Contacts.SetHAPersonEntity(uuid.MustParse(fx.contactID), ""); err != nil {
		t.Fatalf("clear HA binding: %v", err)
	}
	out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := decodeWhereabouts(t, out)
	if res.BestSource != "" {
		t.Errorf("BestSource = %q, want none when every source is withdrawn", res.BestSource)
	}
	if !strings.Contains(res.Basis, "no available whereabouts source") {
		t.Errorf("basis treats withdrawn provenance as a location: %q", res.Basis)
	}
}

func TestContactWhereaboutsHABindingReadFailure(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := contacts.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert(&contacts.Contact{FormattedName: "Alice Operator"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_contacts_ha_person_unique`); err != nil {
		t.Fatalf("drop HA binding index: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE contacts DROP COLUMN ha_person_entity`); err != nil {
		t.Fatalf("drop HA binding column: %v", err)
	}

	_, err = handleContactWhereabouts(context.Background(), CounterpartyToolDeps{Contacts: store}, "Alice Operator", "")
	if err == nil || !strings.Contains(err.Error(), "read HA person binding") {
		t.Fatalf("binding read failure was collapsed into absence: %v", err)
	}
}

// TestContactWhereaboutsUnknownPresence pins the three-state fix: an
// Unknown tracker state must not claim away ranking's basis.
func TestContactWhereaboutsUnknownPresence(t *testing.T) {
	fx := newWhereaboutsFixture(t, "Unknown", "", map[string]companion.ObservationStatus{"device-1": companion.ObservationAvailable})
	out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := decodeWhereabouts(t, out)
	if res.Sources[0].Source != "companion_location" {
		t.Errorf("unknown presence should lead with the device fix: %+v", res.Sources[0])
	}
	if !strings.Contains(res.Basis, "unknown") || strings.Contains(res.Basis, "not home") {
		t.Errorf("basis asserts unsupported location: %q", res.Basis)
	}
}

// TestContactWhereaboutsBoundButSilent pins the C3 distinction: a
// bound contact whose sources yielded nothing gets a valid empty
// result explaining which binding was silent — never the
// configuration-gap error.
func TestContactWhereaboutsBoundButSilent(t *testing.T) {
	cdb, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cdb.Close() })
	store, err := contacts.NewStore(cdb, nil)
	if err != nil {
		t.Fatal(err)
	}
	alice, err := store.Upsert(&contacts.Contact{FormattedName: "Alice Operator"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetHAPersonEntity(alice.ID, "person.alice"); err != nil {
		t.Fatal(err)
	}
	deps := CounterpartyToolDeps{
		Contacts: store,
		Presence: func(string) (contacts.PersonSnapshot, bool) { return contacts.PersonSnapshot{}, false }, // untracked
	}
	out, err := handleContactWhereabouts(context.Background(), deps, "Alice Operator", "")
	if err != nil {
		t.Fatalf("bound-but-silent must not error: %v", err)
	}
	res := decodeWhereabouts(t, out)
	if len(res.Sources) != 0 || !strings.Contains(res.Basis, "person.track") {
		t.Errorf("silent result should teach the tracking gap: %+v", res)
	}
}

// TestContactWhereaboutsLeadingPayloadOnly pins the result bound: only
// the freshest available fix carries its payload; the rest chain by
// identity into companion_last_known_location.
func TestContactWhereaboutsLeadingPayloadOnly(t *testing.T) {
	fx := newWhereaboutsFixture(t, "not_home", "", nil)
	now := time.Now().UTC()
	store := fx.deps.Companions
	seedLocation(t, store, "alice-acct", "device-old", companion.ObservationAvailable, now.Add(-3*time.Hour))
	seedLocation(t, store, "alice-acct", "device-fresh", companion.ObservationAvailable, now.Add(-10*time.Minute))

	out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := decodeWhereabouts(t, out)
	if res.Sources[0].ClientID != "device-fresh" || res.Sources[0].Location == nil {
		t.Errorf("leading fix wrong or payload-less: %+v", res.Sources[0])
	}
	if res.Sources[1].Source != "companion_location" || res.Sources[1].Location != nil {
		t.Errorf("non-leading fix must not carry a payload: %+v", res.Sources[1])
	}
	for _, src := range res.Sources[:2] {
		if src.Account == "" || src.ClientID == "" || src.DeviceID == "" {
			t.Errorf("chaining identity missing: %+v", src)
		}
	}
}
