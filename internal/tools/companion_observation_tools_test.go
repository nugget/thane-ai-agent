package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func newToolObservationStore(t *testing.T) *companion.ObservationStore {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := companion.NewObservationStore(db, nil)
	if err != nil {
		t.Fatalf("new observation store: %v", err)
	}
	return store
}

func TestIOSLastKnownLocationFreshStaleExpiredAndWithdrawn(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		observedAt   time.Time
		status       companion.ObservationStatus
		availability string
		freshness    string
		wantPayload  bool
	}{
		{name: "fresh", observedAt: now.Add(-time.Hour), status: companion.ObservationAvailable, availability: "available", freshness: "fresh", wantPayload: true},
		{name: "stale", observedAt: now.Add(-3 * time.Hour), status: companion.ObservationAvailable, availability: "available", freshness: "stale", wantPayload: true},
		{name: "expired", observedAt: now.Add(-25 * time.Hour), status: companion.ObservationAvailable, availability: "expired", freshness: "expired"},
		{name: "withdrawn", observedAt: now.Add(-time.Minute), status: companion.ObservationWithdrawn, availability: "withdrawn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newToolObservationStore(t)
			var payload json.RawMessage
			if tt.status == companion.ObservationAvailable {
				payload = json.RawMessage(`{"latitude":41,"longitude":-87,"horizontal_accuracy_m":12}`)
			}
			_, err := store.Ingest(context.Background(), companion.ObservationPrincipal{Account: "nugget", DeviceIdentity: "iphone-1"}, companion.ObservationBatch{
				DeviceMetadata: companion.DeviceMetadata{ClientID: "iphone-1", Platform: "ios"},
				Events: []companion.ObservationEvent{{
					EventID: "11111111-1111-4111-8111-111111111111", Kind: "ios.location",
					SchemaVersion: 1, Status: tt.status, ObservedAt: tt.observedAt, Payload: payload,
				}},
			}, now.Add(-time.Second))
			if err != nil {
				t.Fatalf("ingest: %v", err)
			}
			got, err := handleIOSLastKnownLocation(context.Background(), store, map[string]any{}, now)
			if err != nil {
				t.Fatalf("handle: %v", err)
			}
			var result iosLastKnownLocationResult
			if err := json.Unmarshal([]byte(got), &result); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if result.Availability != tt.availability || result.Freshness != tt.freshness {
				t.Fatalf("result = %+v", result)
			}
			if (len(result.Location) > 0) != tt.wantPayload {
				t.Fatalf("location present = %t, want %t (%s)", len(result.Location) > 0, tt.wantPayload, got)
			}
		})
	}
}

func TestIOSLastKnownLocationNeverObservedAndAmbiguous(t *testing.T) {
	store := newToolObservationStore(t)
	got, err := handleIOSLastKnownLocation(context.Background(), store, map[string]any{}, time.Now())
	if err != nil || !strings.Contains(got, `"availability":"never_observed"`) {
		t.Fatalf("never observed: got %q err=%v", got, err)
	}

	at := time.Now()
	for i, clientID := range []string{"iphone-1", "iphone-2"} {
		_, err := store.Ingest(context.Background(), companion.ObservationPrincipal{Account: "nugget", DeviceIdentity: clientID}, companion.ObservationBatch{
			DeviceMetadata: companion.DeviceMetadata{ClientID: clientID, Platform: "ios"},
			Events: []companion.ObservationEvent{{
				EventID: []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"}[i],
				Kind:    "ios.location", SchemaVersion: 1, ObservedAt: at,
				Payload: json.RawMessage(`{"latitude":41,"longitude":-87}`),
			}},
		}, at)
		if err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}
	if _, err := handleIOSLastKnownLocation(context.Background(), store, map[string]any{}, at); err == nil || !strings.Contains(err.Error(), "retry with account and client_id") {
		t.Fatalf("ambiguous error = %v", err)
	}
}

func TestSetCompanionObservationStoreRegistersTaggedTool(t *testing.T) {
	registry := NewEmptyRegistry()
	registry.SetCompanionObservationStore(newToolObservationStore(t))
	tool := registry.Get("ios_last_known_location")
	if tool == nil {
		t.Fatal("ios_last_known_location not registered")
	}
	if !strings.Contains(strings.Join(tool.Tags, ","), "location") {
		t.Fatalf("tags = %v", tool.Tags)
	}
}
