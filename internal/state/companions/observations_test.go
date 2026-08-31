package companions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func recordObservationDevice(t *testing.T, store *Store, account, clientID string, at time.Time) Device {
	t.Helper()
	if err := store.RecordConnected(context.Background(), account, clientID, companion.DeviceMetadata{Platform: "ios"}, at); err != nil {
		t.Fatalf("record device: %v", err)
	}
	device, found, err := store.Get(context.Background(), account, clientID)
	if err != nil || !found {
		t.Fatalf("get device: found=%t err=%v", found, err)
	}
	return device
}

func observationBatch(clientID, eventID string, observedAt time.Time) companion.ObservationBatch {
	return companion.ObservationBatch{
		ObservationDeviceMetadata: companion.ObservationDeviceMetadata{ClientID: clientID, Platform: "ios"},
		Events: []companion.ObservationEvent{{
			EventID: eventID, Kind: "ios.location", SchemaVersion: 1,
			Status: companion.ObservationAvailable, ObservedAt: observedAt,
			Payload: json.RawMessage(`{"latitude":41,"longitude":-87}`),
		}},
	}
}

func TestObservationIdentityResolvesImmutableDeviceID(t *testing.T) {
	store := newTestStore(t)
	device := recordObservationDevice(t, store, "nugget", "iphone-1", t0)

	deviceID, found, err := store.ResolveObservationIdentity(ctx, "nugget", "iphone-1")
	if err != nil || !found {
		t.Fatalf("resolve identity: found=%t err=%v", found, err)
	}
	if deviceID != device.DeviceID {
		t.Fatalf("device ID = %q, want %q", deviceID, device.DeviceID)
	}
	if _, found, err := store.ResolveObservationIdentity(ctx, "aimee", "iphone-1"); err != nil || found {
		t.Fatalf("cross-account resolve: found=%t err=%v", found, err)
	}
}

func TestIngestObservationsLatestRetryWithdrawalAndLastSeen(t *testing.T) {
	store := newTestStore(t)
	device := recordObservationDevice(t, store, "nugget", "iphone-1", t0)
	principal := companion.ObservationPrincipal{Account: "nugget", DeviceID: device.DeviceID}
	batch := observationBatch("iphone-1", "11111111-1111-4111-8111-111111111111", t1)
	batch.ClientName = "Nugget's iPhone"
	batch.AppVersion = "1.1 (2)"
	batch.OSVersion = "26.6"

	result, err := store.IngestObservations(ctx, principal, batch, t1.Add(time.Second))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if result.Stored != 1 || result.Ignored != 0 {
		t.Fatalf("ingest result = %+v", result)
	}

	result, err = store.IngestObservations(ctx, principal, batch, t1.Add(2*time.Second))
	if err != nil || result.Stored != 0 || result.Ignored != 1 {
		t.Fatalf("retry result = %+v err=%v", result, err)
	}
	sameEventNewer := observationBatch("iphone-1", batch.Events[0].EventID, t1.Add(2*time.Second))
	sameEventNewer.Events[0].Payload = json.RawMessage(`{"latitude":0,"longitude":0}`)
	result, err = store.IngestObservations(ctx, principal, sameEventNewer, t1.Add(3*time.Second))
	if err != nil || result.Stored != 0 || result.Ignored != 1 {
		t.Fatalf("mutated retry result = %+v err=%v", result, err)
	}
	latestBeforeWithdrawal, err := store.ResolveLatestObservation(ctx, "nugget", "iphone-1", "ios.location")
	if err != nil || !latestBeforeWithdrawal.ObservedAt.Equal(t1) || string(latestBeforeWithdrawal.Payload) != `{"latitude":41,"longitude":-87}` {
		t.Fatalf("same event ID rewrote latest observation: %+v err=%v", latestBeforeWithdrawal, err)
	}

	older := observationBatch("iphone-1", "00000000-0000-4000-8000-000000000000", t0)
	result, err = store.IngestObservations(ctx, principal, older, t1.Add(4*time.Second))
	if err != nil || result.Stored != 0 || result.Ignored != 1 {
		t.Fatalf("older result = %+v err=%v", result, err)
	}

	withdrawnAt := t2
	withdrawal := observationBatch("iphone-1", "22222222-2222-4222-8222-222222222222", withdrawnAt)
	withdrawal.Events[0].Status = companion.ObservationWithdrawn
	withdrawal.Events[0].Payload = nil
	if _, err := store.IngestObservations(ctx, principal, withdrawal, withdrawnAt.Add(time.Second)); err != nil {
		t.Fatalf("withdraw: %v", err)
	}

	latest, err := store.ResolveLatestObservation(ctx, "nugget", "iphone-1", "ios.location")
	if err != nil {
		t.Fatalf("resolve latest: %v", err)
	}
	if latest.DeviceID != device.DeviceID || latest.Status != companion.ObservationWithdrawn || latest.Payload != nil || !latest.ObservedAt.Equal(withdrawnAt) {
		t.Fatalf("latest = %+v payload=%s", latest, latest.Payload)
	}
	updated, found, err := store.Get(ctx, "nugget", "iphone-1")
	if err != nil || !found || !updated.LastSeenAt.Equal(withdrawnAt.Add(time.Second)) {
		t.Fatalf("last seen = %v found=%t err=%v", updated.LastSeenAt, found, err)
	}
	if updated.ClientName != "Nugget's iPhone" || updated.AppVersion != "1.1 (2)" || updated.OSVersion != "26.6" ||
		!updated.MetadataRecordedAt.Equal(withdrawnAt.Add(time.Second)) {
		t.Fatalf("observation metadata was not persisted: %+v", updated)
	}
}

func TestIngestObservationsEqualTimeWithdrawalDominates(t *testing.T) {
	store := newTestStore(t)
	device := recordObservationDevice(t, store, "nugget", "iphone-1", t0)
	principal := companion.ObservationPrincipal{Account: "nugget", DeviceID: device.DeviceID}
	available := observationBatch("iphone-1", "ffffffff-ffff-4fff-bfff-ffffffffffff", t1)
	if _, err := store.IngestObservations(ctx, principal, available, t1); err != nil {
		t.Fatalf("store available observation: %v", err)
	}

	withdrawn := observationBatch("iphone-1", "00000000-0000-4000-8000-000000000000", t1)
	withdrawn.Events[0].Status = companion.ObservationWithdrawn
	withdrawn.Events[0].Payload = nil
	if _, err := store.IngestObservations(ctx, principal, withdrawn, t1.Add(time.Second)); err != nil {
		t.Fatalf("store equal-time withdrawal: %v", err)
	}
	latest, err := store.ResolveLatestObservation(ctx, "nugget", "iphone-1", "ios.location")
	if err != nil || latest.Status != companion.ObservationWithdrawn || latest.Payload != nil {
		t.Fatalf("latest after withdrawal = %+v payload=%s err=%v", latest, latest.Payload, err)
	}

	resurrection := observationBatch("iphone-1", "ffffffff-ffff-4fff-bfff-ffffffffffff", t1)
	result, err := store.IngestObservations(ctx, principal, resurrection, t1.Add(2*time.Second))
	if err != nil || result.Stored != 0 || result.Ignored != 1 {
		t.Fatalf("equal-time resurrection result = %+v err=%v", result, err)
	}
	latest, err = store.ResolveLatestObservation(ctx, "nugget", "iphone-1", "ios.location")
	if err != nil || latest.Status != companion.ObservationWithdrawn {
		t.Fatalf("equal-time available resurrected withdrawal: %+v err=%v", latest, err)
	}

	recovery := observationBatch("iphone-1", "11111111-1111-4111-8111-111111111111", t2)
	result, err = store.IngestObservations(ctx, principal, recovery, t2.Add(time.Second))
	if err != nil || result.Stored != 1 || result.Ignored != 0 {
		t.Fatalf("strictly newer recovery result = %+v err=%v", result, err)
	}
	latest, err = store.ResolveLatestObservation(ctx, "nugget", "iphone-1", "ios.location")
	if err != nil || latest.Status != companion.ObservationAvailable || latest.Payload == nil || !latest.ObservedAt.Equal(t2) {
		t.Fatalf("strictly newer observation did not recover withdrawal: %+v err=%v", latest, err)
	}
}

func TestIngestObservationsBoundsKindsAtomically(t *testing.T) {
	store := newTestStore(t)
	device := recordObservationDevice(t, store, "nugget", "iphone-1", t0)
	principal := companion.ObservationPrincipal{Account: "nugget", DeviceID: device.DeviceID}
	for i := 0; i < companion.MaxObservationKindsPerDevice; i++ {
		batch := observationBatch("iphone-1", fmt.Sprintf("%08x-0000-4000-8000-000000000000", i), t1)
		batch.Events[0].Kind = fmt.Sprintf("ios.kind.%02d", i)
		if _, err := store.IngestObservations(ctx, principal, batch, t1); err != nil {
			t.Fatalf("seed kind %d: %v", i, err)
		}
	}

	atCap := observationBatch("iphone-1", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", t2)
	atCap.Events[0].Kind = "ios.kind.00"
	result, err := store.IngestObservations(ctx, principal, atCap, t2)
	if err != nil || result.Stored != 1 || result.Ignored != 0 {
		t.Fatalf("existing kind at cap result = %+v err=%v", result, err)
	}

	batch := observationBatch("iphone-1", "ffffffff-ffff-4fff-bfff-ffffffffffff", t2.Add(time.Second))
	batch.Events[0].Kind = "ios.kind.00"
	newKind := batch.Events[0]
	newKind.EventID = "eeeeeeee-eeee-4eee-beee-eeeeeeeeeeee"
	newKind.Kind = "ios.kind.overflow"
	batch.Events = append(batch.Events, newKind)
	if _, err := store.IngestObservations(ctx, principal, batch, t2); !errors.Is(err, companion.ErrObservationKindLimit) {
		t.Fatalf("overflow error = %v", err)
	}
	latest, err := store.ResolveLatestObservation(ctx, "nugget", "iphone-1", "ios.kind.00")
	if err != nil || latest.EventID != atCap.Events[0].EventID {
		t.Fatalf("kind-limit failure was not atomic: latest=%+v err=%v", latest, err)
	}
}

func TestDeviceMetadataRecencyAcrossTransports(t *testing.T) {
	store := newTestStore(t)
	if err := store.RecordConnected(ctx, "nugget", "iphone-1", companion.DeviceMetadata{
		ClientName: "WebSocket old", Platform: "ios", AppVersion: "1.0",
	}, t1); err != nil {
		t.Fatalf("record initial WebSocket metadata: %v", err)
	}
	device, found, err := store.Get(ctx, "nugget", "iphone-1")
	if err != nil || !found {
		t.Fatalf("get device: found=%t err=%v", found, err)
	}
	principal := companion.ObservationPrincipal{Account: "nugget", DeviceID: device.DeviceID}
	httpBatch := observationBatch("iphone-1", "11111111-1111-4111-8111-111111111111", t2)
	httpBatch.ClientName = "HTTP newest"
	httpBatch.AppVersion = "2.0"
	if _, err := store.IngestObservations(ctx, principal, httpBatch, t2); err != nil {
		t.Fatalf("ingest HTTP metadata: %v", err)
	}
	if err := store.RecordConnected(ctx, "nugget", "iphone-1", companion.DeviceMetadata{
		ClientName: "Late stale WebSocket", AppVersion: "1.5",
	}, t0); err != nil {
		t.Fatalf("record stale WebSocket metadata: %v", err)
	}
	if err := store.RecordConnected(ctx, "nugget", "iphone-1", companion.DeviceMetadata{}, t2.Add(time.Hour)); err != nil {
		t.Fatalf("record metadata-free WebSocket connect: %v", err)
	}

	updated, found, err := store.Get(ctx, "nugget", "iphone-1")
	if err != nil || !found {
		t.Fatalf("get updated device: found=%t err=%v", found, err)
	}
	if updated.ClientName != "HTTP newest" || updated.AppVersion != "2.0" || !updated.MetadataRecordedAt.Equal(t2) {
		t.Fatalf("cross-transport metadata did not converge: %+v", updated)
	}
}

func TestConcurrentObservationIngestsSerializeBeforeReading(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	device := recordObservationDevice(t, store, "nugget", "iphone-1", t0)
	principal := companion.ObservationPrincipal{Account: "nugget", DeviceID: device.DeviceID}

	start := make(chan struct{})
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			batch := observationBatch("iphone-1", fmt.Sprintf("%08x-0000-4000-8000-000000000000", i), t1)
			batch.Events[0].Kind = fmt.Sprintf("ios.concurrent.%d", i)
			_, err := store.IngestObservations(context.Background(), principal, batch, t1.Add(time.Duration(i)*time.Second))
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ingest: %v", err)
		}
	}
	observations, err := store.ListLatestObservations(ctx)
	if err != nil || len(observations) != 8 {
		t.Fatalf("observations after concurrent ingest = %d err=%v", len(observations), err)
	}
}

func TestIngestObservationsRejectsCrossAccountPrincipal(t *testing.T) {
	store := newTestStore(t)
	device := recordObservationDevice(t, store, "nugget", "iphone-1", t0)
	_, err := store.IngestObservations(
		ctx,
		companion.ObservationPrincipal{Account: "aimee", DeviceID: device.DeviceID},
		observationBatch("iphone-1", "11111111-1111-4111-8111-111111111111", t1),
		t1,
	)
	if err == nil {
		t.Fatal("expected cross-account principal rejection")
	}
	observations, listErr := store.ListLatestObservations(ctx)
	if listErr != nil || len(observations) != 0 {
		t.Fatalf("observations = %+v err=%v", observations, listErr)
	}
}

func TestObservationStoreScopesAndRequiresUnambiguousDevice(t *testing.T) {
	store := newTestStore(t)
	accounts := []string{"nugget", "aimee"}
	for index, account := range accounts {
		device := recordObservationDevice(t, store, account, "shared-client", t0)
		batch := observationBatch("shared-client", []string{
			"11111111-1111-4111-8111-111111111111",
			"22222222-2222-4222-8222-222222222222",
		}[index], t1)
		batch.Events[0].Payload = json.RawMessage([]byte(`{"owner":"` + account + `"}`))
		if _, err := store.IngestObservations(ctx, companion.ObservationPrincipal{Account: account, DeviceID: device.DeviceID}, batch, t1); err != nil {
			t.Fatalf("ingest %s: %v", account, err)
		}
	}

	if _, err := store.ResolveLatestObservation(ctx, "", "", "ios.location"); !errors.Is(err, companion.ErrObservationAmbiguous) {
		t.Fatalf("ambiguous resolve error = %v", err)
	}
	for _, account := range accounts {
		latest, err := store.ResolveLatestObservation(ctx, account, "shared-client", "ios.location")
		if err != nil {
			t.Fatalf("resolve %s: %v", account, err)
		}
		if string(latest.Payload) != `{"owner":"`+account+`"}` {
			t.Fatalf("%s payload = %s", account, latest.Payload)
		}
	}
}

func TestResolveLatestObservationNotFoundDescribesRoutingScope(t *testing.T) {
	store := newTestStore(t)
	tests := []struct {
		name     string
		account  string
		clientID string
		want     string
	}{
		{name: "unscoped", want: "across all companions"},
		{name: "account", account: "nugget", want: `for account "nugget"`},
		{name: "client", clientID: "iphone-1", want: `for client_id "iphone-1"`},
		{name: "account and client", account: "nugget", clientID: "iphone-1", want: `for account "nugget" and client_id "iphone-1"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.ResolveLatestObservation(ctx, test.account, test.clientID, "ios.location")
			if !errors.Is(err, companion.ErrObservationNotFound) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("not-found error = %v, want scope %q", err, test.want)
			}
		})
	}
}

func TestObservationsSurviveDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "thane.db")
	db, err := database.Open(path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	store, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	device := recordObservationDevice(t, store, "nugget", "iphone-1", t0)
	if _, err := store.IngestObservations(
		ctx,
		companion.ObservationPrincipal{Account: "nugget", DeviceID: device.DeviceID},
		observationBatch("iphone-1", "11111111-1111-4111-8111-111111111111", t1),
		t1.Add(time.Second),
	); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := database.Open(path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restored, err := NewStore(reopened, nil)
	if err != nil {
		t.Fatalf("restore store: %v", err)
	}
	latest, err := restored.ResolveLatestObservation(ctx, "nugget", "iphone-1", "ios.location")
	if err != nil {
		t.Fatalf("resolve after reopen: %v", err)
	}
	if latest.DeviceID != device.DeviceID || !latest.ObservedAt.Equal(t1) {
		t.Fatalf("restored latest = %+v", latest)
	}
}

// TestLatestObservationsByKindScopes pins the counterparty-scoped
// query: only the requested kind and accounts come back.
func TestLatestObservationsByKindScopes(t *testing.T) {
	store := newTestStore(t)
	seedObservation(t, store, "alice", "device-1", "ios.location", companion.ObservationAvailable, t0, t0.Add(time.Minute))
	seedObservation(t, store, "alice", "device-1", "ios.system-context", companion.ObservationAvailable, t0, t0.Add(time.Minute))
	seedObservation(t, store, "bob", "device-2", "ios.location", companion.ObservationAvailable, t1, t1.Add(time.Minute))

	got, err := store.LatestObservationsByKind(ctx, "ios.location", []string{"alice"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 || got[0].Account != "alice" || got[0].Kind != "ios.location" {
		t.Fatalf("scoped query returned %+v", got)
	}
	if empty, err := store.LatestObservationsByKind(ctx, "ios.location", nil); err != nil || empty != nil {
		t.Errorf("no accounts should return nothing: %v %v", empty, err)
	}
}
