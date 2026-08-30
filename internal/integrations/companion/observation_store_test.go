package companion

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func newTestObservationStore(t *testing.T) *ObservationStore {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewObservationStore(db, nil)
	if err != nil {
		t.Fatalf("new observation store: %v", err)
	}
	return store
}

func testObservationPrincipal(account, deviceIdentity string) ObservationPrincipal {
	return ObservationPrincipal{Account: account, DeviceIdentity: deviceIdentity}
}

func TestObservationStoreSurvivesDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "thane.db")
	db, err := database.Open(path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	store, err := NewObservationStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	_, err = store.Ingest(context.Background(), testObservationPrincipal("nugget", "iphone-1"), ObservationBatch{
		DeviceMetadata: DeviceMetadata{ClientID: "iphone-1", Platform: "ios"},
		Events: []ObservationEvent{{
			EventID: "11111111-1111-4111-8111-111111111111", Kind: "ios.location",
			SchemaVersion: 1, ObservedAt: at,
			Payload: json.RawMessage(`{"latitude":41,"longitude":-87}`),
		}},
	}, at.Add(time.Second))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	reopened, err := database.Open(path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restored, err := NewObservationStore(reopened, nil)
	if err != nil {
		t.Fatalf("restore store: %v", err)
	}
	latest, err := restored.ResolveLatest(context.Background(), "nugget", "iphone-1", "ios.location")
	if err != nil {
		t.Fatalf("resolve after reopen: %v", err)
	}
	if !latest.ObservedAt.Equal(at) || string(latest.Payload) != `{"latitude":41,"longitude":-87}` {
		t.Fatalf("restored latest = %+v payload=%s", latest, latest.Payload)
	}
	devices, err := restored.ListDevices(context.Background())
	if err != nil || len(devices) != 1 {
		t.Fatalf("restored devices = %+v err=%v", devices, err)
	}
}

func TestObservationStoreScopesSameClientIDByAccount(t *testing.T) {
	store := newTestObservationStore(t)
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	for index, account := range []string{"nugget", "aimee"} {
		_, err := store.Ingest(context.Background(), testObservationPrincipal(account, "shared-client"), ObservationBatch{
			DeviceMetadata: DeviceMetadata{ClientID: "shared-client", Platform: "ios"},
			Events: []ObservationEvent{{
				EventID: []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"}[index],
				Kind:    "ios.location", SchemaVersion: 1, ObservedAt: at,
				Payload: json.RawMessage([]byte(`{"owner":"` + account + `"}`)),
			}},
		}, at)
		if err != nil {
			t.Fatalf("ingest %s: %v", account, err)
		}
	}

	for _, account := range []string{"nugget", "aimee"} {
		latest, err := store.ResolveLatest(context.Background(), account, "shared-client", "ios.location")
		if err != nil {
			t.Fatalf("resolve %s: %v", account, err)
		}
		if string(latest.Payload) != `{"owner":"`+account+`"}` {
			t.Fatalf("%s payload = %s", account, latest.Payload)
		}
	}
}

func TestObservationStoreKeysByAuthenticatedDeviceIdentity(t *testing.T) {
	store := newTestObservationStore(t)
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	batch := ObservationBatch{
		DeviceMetadata: DeviceMetadata{ClientID: "iphone-1", Platform: "ios"},
		Events: []ObservationEvent{{
			EventID: "11111111-1111-4111-8111-111111111111", Kind: "ios.location",
			SchemaVersion: 1, ObservedAt: at, Payload: json.RawMessage(`{"latitude":41}`),
		}},
	}
	principal := testObservationPrincipal("nugget", "key-fingerprint-1")
	if _, err := store.Ingest(context.Background(), principal, batch, at); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	devices, err := store.ListDevices(context.Background())
	if err != nil || len(devices) != 1 {
		t.Fatalf("devices = %+v err=%v", devices, err)
	}
	if devices[0].DeviceIdentity != principal.DeviceIdentity || devices[0].ClientID != batch.ClientID {
		t.Fatalf("device = %+v", devices[0])
	}
	latest, err := store.ResolveLatest(context.Background(), "nugget", batch.ClientID, "ios.location")
	if err != nil {
		t.Fatalf("resolve by client_id: %v", err)
	}
	if latest.DeviceIdentity != principal.DeviceIdentity || latest.ClientID != batch.ClientID {
		t.Fatalf("latest = %+v", latest)
	}
}

func TestObservationStoreIngestLatestAndWithdrawal(t *testing.T) {
	store := newTestObservationStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	batch := ObservationBatch{
		DeviceMetadata: DeviceMetadata{
			ClientID: "iphone-1", ClientName: "Thane for iOS", Platform: "ios",
			AppVersion: "1.0", OSVersion: "26.6",
		},
		Events: []ObservationEvent{{
			EventID: "11111111-1111-4111-8111-111111111111", Kind: "ios.location",
			SchemaVersion: 1, Status: ObservationAvailable, ObservedAt: base,
			Payload: json.RawMessage(`{"latitude":41,"longitude":-87}`),
		}},
	}

	principal := testObservationPrincipal("nugget", batch.ClientID)
	result, err := store.Ingest(ctx, principal, batch, base.Add(time.Second))
	if err != nil {
		t.Fatalf("ingest available: %v", err)
	}
	if result.Stored != 1 || result.Ignored != 0 {
		t.Fatalf("available result = %+v", result)
	}

	// Exact retry is idempotent.
	result, err = store.Ingest(ctx, principal, batch, base.Add(2*time.Second))
	if err != nil {
		t.Fatalf("retry ingest: %v", err)
	}
	if result.Stored != 0 || result.Ignored != 1 {
		t.Fatalf("retry result = %+v", result)
	}

	// An older observation cannot regress the latest row.
	older := batch
	older.Events = []ObservationEvent{{
		EventID: "00000000-0000-4000-8000-000000000000", Kind: "ios.location",
		SchemaVersion: 1, Status: ObservationAvailable, ObservedAt: base.Add(-time.Hour),
		Payload: json.RawMessage(`{"latitude":0,"longitude":0}`),
	}}
	result, err = store.Ingest(ctx, principal, older, base.Add(3*time.Second))
	if err != nil {
		t.Fatalf("older ingest: %v", err)
	}
	if result.Stored != 0 || result.Ignored != 1 {
		t.Fatalf("older result = %+v", result)
	}

	withdrawnAt := base.Add(time.Hour)
	withdrawal := batch
	withdrawal.Events = []ObservationEvent{{
		EventID: "22222222-2222-4222-8222-222222222222", Kind: "ios.location",
		SchemaVersion: 1, Status: ObservationWithdrawn, ObservedAt: withdrawnAt,
	}}
	if _, err := store.Ingest(ctx, principal, withdrawal, withdrawnAt.Add(time.Second)); err != nil {
		t.Fatalf("withdraw ingest: %v", err)
	}

	latest, err := store.ResolveLatest(ctx, "nugget", "iphone-1", "ios.location")
	if err != nil {
		t.Fatalf("resolve latest: %v", err)
	}
	if latest.Status != ObservationWithdrawn || latest.Payload != nil || !latest.ObservedAt.Equal(withdrawnAt) {
		t.Fatalf("latest = %+v payload=%s", latest, latest.Payload)
	}

	devices, err := store.ListDevices(ctx)
	if err != nil {
		t.Fatalf("list devices: %v", err)
	}
	if len(devices) != 1 || devices[0].DeviceIdentity != "iphone-1" || devices[0].Platform != "ios" || devices[0].ClientName != "Thane for iOS" {
		t.Fatalf("devices = %+v", devices)
	}
}

func TestObservationStoreConnectionLifecyclePersistsDevice(t *testing.T) {
	store := newTestObservationStore(t)
	ctx := context.Background()
	connectedAt := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	disconnectedAt := connectedAt.Add(5 * time.Minute)
	device := DeviceMetadata{ClientID: "mac-1", ClientName: "Office Mac", Platform: "macos"}

	principal := testObservationPrincipal("nugget", device.ClientID)
	if err := store.RecordConnected(ctx, principal, device, connectedAt); err != nil {
		t.Fatalf("record connected: %v", err)
	}
	if err := store.RecordDisconnected(ctx, principal, disconnectedAt); err != nil {
		t.Fatalf("record disconnected: %v", err)
	}
	devices, err := store.ListDevices(ctx)
	if err != nil {
		t.Fatalf("list devices: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("devices = %+v", devices)
	}
	if devices[0].LastConnectedAt == nil || !devices[0].LastConnectedAt.Equal(connectedAt) {
		t.Fatalf("last connected = %v", devices[0].LastConnectedAt)
	}
	if devices[0].LastDisconnectedAt == nil || !devices[0].LastDisconnectedAt.Equal(disconnectedAt) {
		t.Fatalf("last disconnected = %v", devices[0].LastDisconnectedAt)
	}
}

func TestObservationStoreResolveLatestRequiresUnambiguousDevice(t *testing.T) {
	store := newTestObservationStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	for i, clientID := range []string{"iphone-1", "iphone-2"} {
		batch := ObservationBatch{
			DeviceMetadata: DeviceMetadata{ClientID: clientID, Platform: "ios"},
			Events: []ObservationEvent{{
				EventID: []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"}[i],
				Kind:    "ios.location", SchemaVersion: 1, ObservedAt: at,
				Payload: json.RawMessage(`{"latitude":41,"longitude":-87}`),
			}},
		}
		if _, err := store.Ingest(ctx, testObservationPrincipal("nugget", clientID), batch, at); err != nil {
			t.Fatalf("ingest %s: %v", clientID, err)
		}
	}

	if _, err := store.ResolveLatest(ctx, "", "", "ios.location"); err == nil {
		t.Fatal("expected ambiguous resolve error")
	}
	if _, err := store.ResolveLatest(ctx, "nugget", "iphone-2", "ios.location"); err != nil {
		t.Fatalf("targeted resolve: %v", err)
	}
}
