package companions

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func testObservation(eventID string, observedAt time.Time) companion.Observation {
	return companion.Observation{
		EventID:       eventID,
		Kind:          "ios.location",
		SchemaVersion: 1,
		ObservedAt:    observedAt,
		ReceivedAt:    observedAt.Add(time.Minute),
		Payload:       []byte(`{"latitude":41.0,"longitude":-87.0}`),
	}
}

func TestEnsureDeviceCreatesAndReuses(t *testing.T) {
	store := newTestStore(t)

	// First contact over HTTPS: the row is created with seen stamps
	// only — an upload is not a connection.
	id1, err := store.EnsureDevice(ctx, "alice", "device-1", t0)
	if err != nil {
		t.Fatalf("EnsureDevice: %v", err)
	}
	d, ok, err := store.Get(ctx, "alice", "device-1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if d.DeviceID != id1 {
		t.Errorf("DeviceID = %q, want %q", d.DeviceID, id1)
	}
	if !d.FirstSeenAt.Equal(t0) || !d.LastSeenAt.Equal(t0) {
		t.Errorf("seen stamps: first=%v last=%v, want %v", d.FirstSeenAt, d.LastSeenAt, t0)
	}
	if !d.LastConnectedAt.IsZero() {
		t.Errorf("EnsureDevice stamped a connection time: %v", d.LastConnectedAt)
	}

	// A later upload reuses the identity and bumps last_seen.
	id2, err := store.EnsureDevice(ctx, "alice", "device-1", t1)
	if err != nil {
		t.Fatalf("EnsureDevice again: %v", err)
	}
	if id2 != id1 {
		t.Errorf("device identity changed across uploads: %q → %q", id1, id2)
	}
	d, _, _ = store.Get(ctx, "alice", "device-1")
	if !d.FirstSeenAt.Equal(t0) || !d.LastSeenAt.Equal(t1) {
		t.Errorf("seen stamps after second upload: first=%v last=%v", d.FirstSeenAt, d.LastSeenAt)
	}
}

func TestEnsureDeviceMatchesConnectedDevice(t *testing.T) {
	store := newTestStore(t)
	if err := store.RecordConnected(ctx, "alice", "device-1", companion.DeviceMetadata{Platform: "ios"}, t0); err != nil {
		t.Fatalf("RecordConnected: %v", err)
	}
	viaWS, _, err := store.Get(ctx, "alice", "device-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	viaHTTP, err := store.EnsureDevice(ctx, "alice", "device-1", t1)
	if err != nil {
		t.Fatalf("EnsureDevice: %v", err)
	}
	if viaHTTP != viaWS.DeviceID {
		t.Errorf("HTTPS upload resolved a different device: %q vs %q", viaHTTP, viaWS.DeviceID)
	}
}

func TestUpsertObservationLatestOnly(t *testing.T) {
	store := newTestStore(t)
	deviceID, err := store.EnsureDevice(ctx, "alice", "device-1", t0)
	if err != nil {
		t.Fatalf("EnsureDevice: %v", err)
	}

	// First observation applies.
	out, err := store.UpsertObservation(ctx, deviceID, testObservation("evt-1", t0))
	if err != nil || out != companion.ObservationApplied {
		t.Fatalf("first upsert: outcome=%v err=%v", out, err)
	}

	// Exact replay is a duplicate and writes nothing.
	out, err = store.UpsertObservation(ctx, deviceID, testObservation("evt-1", t0))
	if err != nil || out != companion.ObservationDuplicate {
		t.Fatalf("replay: outcome=%v err=%v", out, err)
	}

	// A newer observation replaces the row.
	newer := testObservation("evt-2", t1)
	newer.Payload = []byte(`{"latitude":42.0,"longitude":-88.0}`)
	out, err = store.UpsertObservation(ctx, deviceID, newer)
	if err != nil || out != companion.ObservationApplied {
		t.Fatalf("newer upsert: outcome=%v err=%v", out, err)
	}

	// An older distinct event is superseded and cannot regress state.
	out, err = store.UpsertObservation(ctx, deviceID, testObservation("evt-0", t0))
	if err != nil || out != companion.ObservationSuperseded {
		t.Fatalf("older upsert: outcome=%v err=%v", out, err)
	}

	obs, ok, err := store.GetObservation(ctx, deviceID, "ios.location")
	if err != nil || !ok {
		t.Fatalf("GetObservation: ok=%v err=%v", ok, err)
	}
	if obs.EventID != "evt-2" || !obs.ObservedAt.Equal(t1) {
		t.Errorf("stored observation = %s @ %v, want evt-2 @ %v", obs.EventID, obs.ObservedAt, t1)
	}
	if string(obs.Payload) != `{"latitude":42.0,"longitude":-88.0}` {
		t.Errorf("payload = %s", obs.Payload)
	}
	if !obs.ReceivedAt.Equal(t1.Add(time.Minute)) {
		t.Errorf("ReceivedAt = %v, want independent receipt time %v", obs.ReceivedAt, t1.Add(time.Minute))
	}
}

func TestWithdrawalClearsPayload(t *testing.T) {
	store := newTestStore(t)
	deviceID, err := store.EnsureDevice(ctx, "alice", "device-1", t0)
	if err != nil {
		t.Fatalf("EnsureDevice: %v", err)
	}
	if _, err := store.UpsertObservation(ctx, deviceID, testObservation("evt-1", t0)); err != nil {
		t.Fatalf("seed observation: %v", err)
	}

	withdrawal := companion.Observation{
		EventID:       "evt-2",
		Kind:          "ios.location",
		SchemaVersion: 1,
		ObservedAt:    t1,
		ReceivedAt:    t1,
		Withdrawn:     true,
	}
	out, err := store.UpsertObservation(ctx, deviceID, withdrawal)
	if err != nil || out != companion.ObservationApplied {
		t.Fatalf("withdrawal: outcome=%v err=%v", out, err)
	}

	obs, ok, err := store.GetObservation(ctx, deviceID, "ios.location")
	if err != nil || !ok {
		t.Fatalf("GetObservation: ok=%v err=%v", ok, err)
	}
	if !obs.Withdrawn {
		t.Error("observation not marked withdrawn")
	}
	if string(obs.Payload) != "{}" {
		t.Errorf("withdrawn payload = %s, want cleared", obs.Payload)
	}

	// Sharing re-enabled: a newer present observation resurrects data.
	restored := testObservation("evt-3", t2)
	out, err = store.UpsertObservation(ctx, deviceID, restored)
	if err != nil || out != companion.ObservationApplied {
		t.Fatalf("restore: outcome=%v err=%v", out, err)
	}
	obs, _, _ = store.GetObservation(ctx, deviceID, "ios.location")
	if obs.Withdrawn || string(obs.Payload) == "{}" {
		t.Errorf("restore did not take: withdrawn=%v payload=%s", obs.Withdrawn, obs.Payload)
	}
}

func TestObservationsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "thane.db")
	db, err := database.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	deviceID, err := store.EnsureDevice(ctx, "alice", "device-1", t0)
	if err != nil {
		t.Fatalf("EnsureDevice: %v", err)
	}
	if _, err := store.UpsertObservation(ctx, deviceID, testObservation("evt-1", t0)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db2, err := database.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	store2, err := NewStore(db2, nil)
	if err != nil {
		t.Fatalf("remigrate: %v", err)
	}
	obs, ok, err := store2.GetObservation(ctx, deviceID, "ios.location")
	if err != nil || !ok {
		t.Fatalf("observation lost across restart: ok=%v err=%v", ok, err)
	}
	if obs.EventID != "evt-1" || !obs.ObservedAt.Equal(t0) {
		t.Errorf("observation degraded across restart: %+v", obs)
	}
}
