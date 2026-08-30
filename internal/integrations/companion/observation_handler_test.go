package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recordingObservationStore struct {
	principal ObservationPrincipal
	batch     ObservationBatch
	err       error
}

type recordingObservationAuthenticator struct {
	request ObservationAuthRequest
}

func (a *recordingObservationAuthenticator) AuthenticateObservation(_ context.Context, request ObservationAuthRequest) (ObservationPrincipal, error) {
	a.request = request
	return ObservationPrincipal{Account: "nugget", DeviceID: "dev_iphone_1"}, nil
}

func (s *recordingObservationStore) IngestObservations(
	_ context.Context,
	principal ObservationPrincipal,
	batch ObservationBatch,
	receivedAt time.Time,
) (IngestResult, error) {
	s.principal = principal
	s.batch = batch
	if s.err != nil {
		return IngestResult{}, s.err
	}
	return IngestResult{Stored: len(batch.Events), ReceivedAt: receivedAt}, nil
}

func testObservationAuthenticator() ObservationAuthenticator {
	identities := fakeObservationIdentityResolver{devices: map[string]string{
		"nugget/iphone-1": "dev_iphone_1",
	}}
	return NewBearerObservationAuthenticator(testTokenIndex(), identities.ResolveObservationIdentity)
}

func TestObservationHandlerAcceptsAuthenticatedBatch(t *testing.T) {
	store := &recordingObservationStore{}
	authenticator := &recordingObservationAuthenticator{}
	handler := NewObservationHandler(authenticator, store, nil)
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

	req := httptest.NewRequest(http.MethodPost, "https://thane.example/v1/companion/observations?source=background", strings.NewReader(body))
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
	if store.principal != (ObservationPrincipal{Account: "nugget", DeviceID: "dev_iphone_1"}) {
		t.Fatalf("principal = %+v", store.principal)
	}
	if store.batch.ClientID != "iphone-1" || len(store.batch.Events) != 1 {
		t.Fatalf("batch = %+v", store.batch)
	}
	if authenticator.request.Method != http.MethodPost || authenticator.request.RequestTarget != "/v1/companion/observations?source=background" {
		t.Fatalf("auth request target = %s %s", authenticator.request.Method, authenticator.request.RequestTarget)
	}
	if authenticator.request.Scheme != "https" || authenticator.request.Authority != "thane.example" ||
		authenticator.request.TargetURI != "https://thane.example/v1/companion/observations?source=background" {
		t.Fatalf("auth request URI components = scheme %q authority %q target %q",
			authenticator.request.Scheme, authenticator.request.Authority, authenticator.request.TargetURI)
	}
	if authenticator.request.Header.Get("Authorization") != "Bearer test-secret" || string(authenticator.request.Body) != body {
		t.Fatal("authenticator did not receive the exact bounded HTTP request")
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
		{name: "unknown device", auth: "Bearer test-secret", contentType: "application/json", body: strings.Replace(valid, "iphone-1", "iphone-2", 1), wantStatus: http.StatusUnauthorized},
		{name: "wrong content type", auth: "Bearer test-secret", contentType: "text/plain", body: valid, wantStatus: http.StatusUnsupportedMediaType},
		{name: "unknown field", auth: "Bearer test-secret", contentType: "application/json", body: strings.Replace(valid, `"client_id"`, `"unknown":true,"client_id"`, 1), wantStatus: http.StatusBadRequest},
		{name: "missing events", auth: "Bearer test-secret", contentType: "application/json", body: `{"client_id":"iphone-1"}`, wantStatus: http.StatusBadRequest},
		{name: "implausibly old observation", auth: "Bearer test-secret", contentType: "application/json", body: strings.Replace(valid, "2026-08-30T11:59:55Z", "1970-01-01T00:00:00Z", 1), wantStatus: http.StatusBadRequest},
		{name: "future observation", auth: "Bearer test-secret", contentType: "application/json", body: strings.Replace(valid, "2026-08-30T11:59:55Z", "2026-08-30T12:06:00Z", 1), wantStatus: http.StatusBadRequest},
		{name: "withdrawal with payload", auth: "Bearer test-secret", contentType: "application/json", body: strings.Replace(valid, `"payload"`, `"status":"withdrawn","payload"`, 1), wantStatus: http.StatusBadRequest},
		{name: "trailing object", auth: "Bearer test-secret", contentType: "application/json", body: valid + `{}`, wantStatus: http.StatusBadRequest},
		{name: "oversized body", auth: "Bearer test-secret", contentType: "application/json", body: `{"padding":"` + strings.Repeat("x", maxObservationBodyBytes) + `"}`, wantStatus: http.StatusRequestEntityTooLarge},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewObservationHandler(testObservationAuthenticator(), &recordingObservationStore{}, nil)
			handler.now = func() time.Time { return now }
			req := httptest.NewRequest(http.MethodPost, "/v1/companion/observations", bytes.NewBufferString(test.body))
			if test.auth != "" {
				req.Header.Set("Authorization", test.auth)
			}
			if test.contentType != "" {
				req.Header.Set("Content-Type", test.contentType)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, test.wantStatus, rec.Body.String())
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
	tooManyProperties := make(map[string]int, maxObservationProperties+1)
	for i := 0; i <= maxObservationProperties; i++ {
		tooManyProperties[fmt.Sprintf("property_%03d", i)] = i
	}
	tooManyPropertiesJSON, err := json.Marshal(tooManyProperties)
	if err != nil {
		t.Fatalf("marshal oversized property set: %v", err)
	}
	tests := []struct {
		name  string
		batch ObservationBatch
	}{
		{name: "empty events", batch: ObservationBatch{ObservationDeviceMetadata: ObservationDeviceMetadata{ClientID: "iphone-1"}}},
		{name: "too many events", batch: ObservationBatch{ObservationDeviceMetadata: ObservationDeviceMetadata{ClientID: "iphone-1"}, Events: make([]ObservationEvent, maxObservationEvents+1)}},
		{name: "oversized payload", batch: ObservationBatch{ObservationDeviceMetadata: ObservationDeviceMetadata{ClientID: "iphone-1"}, Events: []ObservationEvent{{
			EventID: validEvent.EventID, Kind: validEvent.Kind, SchemaVersion: 1, ObservedAt: now,
			Payload: json.RawMessage(`{"value":"` + strings.Repeat("x", maxObservationPayloadBytes) + `"}`),
		}}}},
		{name: "unsupported kind character", batch: ObservationBatch{ObservationDeviceMetadata: ObservationDeviceMetadata{ClientID: "iphone-1"}, Events: []ObservationEvent{{
			EventID: validEvent.EventID, Kind: "ios/location", SchemaVersion: 1, ObservedAt: now, Payload: validEvent.Payload,
		}}}},
		{name: "too many payload properties", batch: ObservationBatch{ObservationDeviceMetadata: ObservationDeviceMetadata{ClientID: "iphone-1"}, Events: []ObservationEvent{{
			EventID: validEvent.EventID, Kind: validEvent.Kind, SchemaVersion: 1, ObservedAt: now, Payload: tooManyPropertiesJSON,
		}}}},
		{name: "overlong metadata", batch: ObservationBatch{ObservationDeviceMetadata: ObservationDeviceMetadata{ClientID: "iphone-1", ClientName: strings.Repeat("x", maxObservationStringRunes+1)}, Events: []ObservationEvent{validEvent}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateObservationBatch(&test.batch, now); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestObservationHandlerReportsUnavailableAuthenticationAndKindLimit(t *testing.T) {
	valid := `{"client_id":"iphone-1","events":[{"event_id":"11111111-1111-4111-8111-111111111111","kind":"ios.location","schema_version":1,"observed_at":"2026-08-30T11:59:55Z","payload":{"latitude":41}}]}`
	tests := []struct {
		name       string
		handler    *ObservationHandler
		wantStatus int
		wantCode   string
	}{
		{
			name:       "authentication unavailable",
			handler:    NewObservationHandler(nil, &recordingObservationStore{}, nil),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "observation_auth_unavailable",
		},
		{
			name: "kind limit",
			handler: NewObservationHandler(
				testObservationAuthenticator(),
				&recordingObservationStore{err: ErrObservationKindLimit},
				nil,
			),
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_observation",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.handler.now = func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }
			req := httptest.NewRequest(http.MethodPost, "/v1/companion/observations", strings.NewReader(valid))
			req.Header.Set("Authorization", "Bearer test-secret")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			test.handler.ServeHTTP(rec, req)
			if rec.Code != test.wantStatus || !strings.Contains(rec.Body.String(), `"code":"`+test.wantCode+`"`) {
				t.Fatalf("response = %d %s, want status %d code %q", rec.Code, rec.Body.String(), test.wantStatus, test.wantCode)
			}
		})
	}
}

func TestValidateObservationBatchCanonicalizesEventID(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	batch := ObservationBatch{
		ObservationDeviceMetadata: ObservationDeviceMetadata{ClientID: "iphone-1"},
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

func TestValidateObservationBatchPreservesOpaqueClientID(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	batch := ObservationBatch{
		ObservationDeviceMetadata: ObservationDeviceMetadata{ClientID: "device:key/+ value"},
		Events: []ObservationEvent{{
			EventID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Kind: "ios.location",
			SchemaVersion: 1, ObservedAt: now, Payload: json.RawMessage(`{"latitude":41}`),
		}},
	}

	if err := validateObservationBatch(&batch, now); err != nil {
		t.Fatalf("validate opaque client ID: %v", err)
	}
	if batch.ClientID != "device:key/+ value" {
		t.Fatalf("client ID rewritten as %q", batch.ClientID)
	}
}
