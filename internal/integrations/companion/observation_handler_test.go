package companion

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestObservationHandlerAcceptsAuthenticatedBatch(t *testing.T) {
	store := newTestObservationStore(t)
	handler := NewObservationHandler(testTokenIndex(), store, nil)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	handler.now = func() time.Time { return now }
	body := `{
		"client_id":"iphone-1",
		"client_name":"Thane for iOS",
		"platform":"ios",
		"app_version":"1.0",
		"os_version":"26.6",
		"events":[{
			"event_id":"11111111-1111-4111-8111-111111111111",
			"kind":"ios.location",
			"schema_version":1,
			"observed_at":"2026-08-30T11:59:55Z",
			"payload":{"latitude":41,"longitude":-87}
		}]
	}`

	req := httptest.NewRequest(http.MethodPost, "/v1/companion/observations", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var result IngestResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Stored != 1 || !result.ReceivedAt.Equal(now) {
		t.Fatalf("result = %+v", result)
	}
	latest, err := store.ResolveLatest(req.Context(), "nugget", "iphone-1", "ios.location")
	if err != nil {
		t.Fatalf("resolve latest: %v", err)
	}
	if latest.Account != "nugget" {
		t.Fatalf("account = %q", latest.Account)
	}
}

func TestObservationHandlerRejectsInvalidRequests(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	valid := `{"client_id":"iphone-1","platform":"ios","events":[{"event_id":"11111111-1111-4111-8111-111111111111","kind":"ios.location","schema_version":1,"observed_at":"2026-08-30T11:59:55Z","payload":{"latitude":41}}]}`
	tests := []struct {
		name        string
		auth        string
		contentType string
		body        string
		wantStatus  int
	}{
		{name: "missing auth", contentType: "application/json", body: valid, wantStatus: http.StatusUnauthorized},
		{name: "wrong auth", auth: "Bearer wrong", contentType: "application/json", body: valid, wantStatus: http.StatusUnauthorized},
		{name: "wrong content type", auth: "Bearer test-secret", contentType: "text/plain", body: valid, wantStatus: http.StatusUnsupportedMediaType},
		{name: "unknown field", auth: "Bearer test-secret", contentType: "application/json", body: strings.Replace(valid, `"client_id"`, `"unknown":true,"client_id"`, 1), wantStatus: http.StatusBadRequest},
		{name: "missing events", auth: "Bearer test-secret", contentType: "application/json", body: `{"client_id":"iphone-1"}`, wantStatus: http.StatusBadRequest},
		{name: "future observation", auth: "Bearer test-secret", contentType: "application/json", body: strings.Replace(valid, "2026-08-30T11:59:55Z", "2026-08-30T12:06:00Z", 1), wantStatus: http.StatusBadRequest},
		{name: "withdrawal with payload", auth: "Bearer test-secret", contentType: "application/json", body: strings.Replace(valid, `"payload"`, `"status":"withdrawn","payload"`, 1), wantStatus: http.StatusBadRequest},
		{name: "trailing object", auth: "Bearer test-secret", contentType: "application/json", body: valid + `{}`, wantStatus: http.StatusBadRequest},
		{name: "oversized body", auth: "Bearer test-secret", contentType: "application/json", body: `{"padding":"` + strings.Repeat("x", maxObservationBodyBytes) + `"}`, wantStatus: http.StatusRequestEntityTooLarge},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewObservationHandler(testTokenIndex(), newTestObservationStore(t), nil)
			handler.now = func() time.Time { return now }
			req := httptest.NewRequest(http.MethodPost, "/v1/companion/observations", bytes.NewBufferString(tc.body))
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestValidateObservationBatchBounds(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	validEvent := ObservationEvent{
		EventID: "11111111-1111-4111-8111-111111111111", Kind: "ios.location",
		SchemaVersion: 1, ObservedAt: now, Payload: json.RawMessage(`{"latitude":41}`),
	}
	tests := []struct {
		name  string
		batch ObservationBatch
	}{
		{name: "empty events", batch: ObservationBatch{DeviceMetadata: DeviceMetadata{ClientID: "iphone-1"}}},
		{name: "too many events", batch: ObservationBatch{DeviceMetadata: DeviceMetadata{ClientID: "iphone-1"}, Events: make([]ObservationEvent, maxObservationEvents+1)}},
		{name: "oversized payload", batch: ObservationBatch{DeviceMetadata: DeviceMetadata{ClientID: "iphone-1"}, Events: []ObservationEvent{{
			EventID: validEvent.EventID, Kind: validEvent.Kind, SchemaVersion: 1, ObservedAt: now,
			Payload: json.RawMessage(`{"value":"` + strings.Repeat("x", maxObservationPayloadBytes) + `"}`),
		}}}},
		{name: "unsupported kind character", batch: ObservationBatch{DeviceMetadata: DeviceMetadata{ClientID: "iphone-1"}, Events: []ObservationEvent{{
			EventID: validEvent.EventID, Kind: "ios/location", SchemaVersion: 1, ObservedAt: now, Payload: validEvent.Payload,
		}}}},
		{name: "overlong metadata", batch: ObservationBatch{DeviceMetadata: DeviceMetadata{ClientID: "iphone-1", ClientName: strings.Repeat("x", maxObservationStringRunes+1)}, Events: []ObservationEvent{validEvent}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateObservationBatch(&test.batch, now); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateObservationBatchCanonicalizesEventID(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	batch := ObservationBatch{
		DeviceMetadata: DeviceMetadata{ClientID: "iphone-1"},
		Events: []ObservationEvent{{
			EventID: "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", Kind: "ios.location",
			SchemaVersion: 1, ObservedAt: now, Payload: json.RawMessage(`{"latitude":41}`),
		}},
	}

	if err := validateObservationBatch(&batch, now); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got, want := batch.Events[0].EventID, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"; got != want {
		t.Fatalf("event ID = %q, want %q", got, want)
	}
}
