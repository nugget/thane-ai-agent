package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/companions"
)

func newObservationToolStore(t *testing.T) *companions.Store {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := companions.NewStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func seedLocation(t *testing.T, store *companions.Store, account, clientID string, status companion.ObservationStatus, observedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := store.RecordConnected(ctx, account, clientID, companion.DeviceMetadata{ClientName: "Alice's iPhone", Platform: "ios"}, observedAt); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	device, ok, err := store.Get(ctx, account, clientID)
	if err != nil || !ok {
		t.Fatalf("get device: ok=%v err=%v", ok, err)
	}
	event := companion.ObservationEvent{
		EventID:       "0d1f8a6e-4c2b-4b7e-9f00-3a7d0e2c9b41",
		Kind:          "ios.location",
		SchemaVersion: 1,
		Status:        status,
		ObservedAt:    observedAt,
	}
	if status == companion.ObservationAvailable {
		event.Payload = json.RawMessage(`{"latitude":41.0,"longitude":-87.0,"horizontal_accuracy_m":24.0}`)
	}
	if _, err := store.IngestObservations(ctx, companion.ObservationPrincipal{Account: account, DeviceID: device.DeviceID},
		companion.ObservationBatch{
			ObservationDeviceMetadata: companion.ObservationDeviceMetadata{ClientID: clientID},
			Events:                    []companion.ObservationEvent{event},
		}, observedAt.Add(time.Minute)); err != nil {
		t.Fatalf("seed observation: %v", err)
	}
}

func adminResolver(_ context.Context, account string) (companions.ContactBinding, bool) {
	if account == "alice" {
		return companions.ContactBinding{ContactID: "c-1", Name: "Alice Operator", TrustZone: "admin"}, true
	}
	return companions.ContactBinding{}, false
}

func decodeLocationResult(t *testing.T, out string) companionLastKnownLocationResult {
	t.Helper()
	var res companionLastKnownLocationResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode result: %v\n%s", err, out)
	}
	return res
}

func TestCompanionLastKnownLocationFresh(t *testing.T) {
	store := newObservationToolStore(t)
	seedLocation(t, store, "alice", "device-1", companion.ObservationAvailable, time.Now().UTC().Add(-30*time.Minute))

	r := &Registry{}
	out, err := r.handleCompanionLastKnownLocation(context.Background(), store, adminResolver, "", "")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := decodeLocationResult(t, out)
	if res.Status != "available" || res.Freshness != "fresh" {
		t.Errorf("status/freshness = %s/%s", res.Status, res.Freshness)
	}
	if res.Contact != "Alice Operator" || res.ContactTrustZone != "admin" {
		t.Errorf("counterparty attribution = %q/%q", res.Contact, res.ContactTrustZone)
	}
	if res.ClientName != "Alice's iPhone" || res.Platform != "ios" || res.DeviceID == "" {
		t.Errorf("device metadata = %+v", res)
	}
	if !strings.Contains(string(res.Location), `"latitude":41`) {
		t.Errorf("payload not passed through: %s", res.Location)
	}
	if res.ObservedAgo == "" || res.ReceivedAgo == "" || res.ObservedAt == "" {
		t.Errorf("ages missing: %+v", res)
	}
}

func TestCompanionLastKnownLocationStale(t *testing.T) {
	store := newObservationToolStore(t)
	seedLocation(t, store, "alice", "device-1", companion.ObservationAvailable, time.Now().UTC().Add(-25*time.Hour))

	r := &Registry{}
	out, err := r.handleCompanionLastKnownLocation(context.Background(), store, nil, "", "")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := decodeLocationResult(t, out)
	if res.Freshness != "stale" {
		t.Errorf("freshness = %q, want stale past 24h", res.Freshness)
	}
	if len(res.Location) == 0 {
		t.Error("stale still returns the payload; the label is a judgment aid, not a gate")
	}
}

func TestCompanionLastKnownLocationWithdrawn(t *testing.T) {
	store := newObservationToolStore(t)
	seedLocation(t, store, "alice", "device-1", companion.ObservationWithdrawn, time.Now().UTC().Add(-time.Hour))

	r := &Registry{}
	out, err := r.handleCompanionLastKnownLocation(context.Background(), store, nil, "", "")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := decodeLocationResult(t, out)
	if res.Status != "withdrawn" || len(res.Location) != 0 {
		t.Errorf("withdrawn result leaked data: %+v", res)
	}
	if res.Note == "" || res.Freshness != "" {
		t.Errorf("withdrawn semantics not taught: %+v", res)
	}
}

func TestCompanionLastKnownLocationNeverObserved(t *testing.T) {
	store := newObservationToolStore(t)
	r := &Registry{}
	_, err := r.handleCompanionLastKnownLocation(context.Background(), store, nil, "", "")
	if err == nil || !strings.Contains(err.Error(), "never pushed") {
		t.Fatalf("never-observed error not distinct: %v", err)
	}
}

func TestCompanionLastKnownLocationAmbiguous(t *testing.T) {
	store := newObservationToolStore(t)
	seedLocation(t, store, "alice", "device-1", companion.ObservationAvailable, time.Now().UTC().Add(-time.Hour))
	seedLocation(t, store, "bob", "device-2", companion.ObservationAvailable, time.Now().UTC().Add(-time.Hour))

	r := &Registry{}
	_, err := r.handleCompanionLastKnownLocation(context.Background(), store, nil, "", "")
	if err == nil || !strings.Contains(err.Error(), "alice") || !strings.Contains(err.Error(), "bob") {
		t.Fatalf("ambiguity error must list candidates: %v", err)
	}

	// Supplying the parameter the error names resolves it.
	out, err := r.handleCompanionLastKnownLocation(context.Background(), store, nil, "bob", "")
	if err != nil {
		t.Fatalf("narrowed call: %v", err)
	}
	if res := decodeLocationResult(t, out); res.Account != "bob" {
		t.Errorf("narrowed to %q, want bob", res.Account)
	}
}
