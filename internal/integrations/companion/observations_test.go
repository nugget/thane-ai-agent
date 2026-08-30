package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeObservationSink records ingestion calls and returns canned
// outcomes.
type fakeObservationSink struct {
	mu           sync.Mutex
	ensured      []string // "account/client_id"
	observations []Observation
	upsertErr    error
	outcome      ObservationOutcome
}

func (f *fakeObservationSink) EnsureDevice(_ context.Context, account, clientID string, _ time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, account+"/"+clientID)
	return "dev_test", nil
}

func (f *fakeObservationSink) UpsertObservation(_ context.Context, _ string, obs Observation) (ObservationOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return "", f.upsertErr
	}
	f.observations = append(f.observations, obs)
	if f.outcome == "" {
		return ObservationApplied, nil
	}
	return f.outcome, nil
}

func newObservationsServer(t *testing.T, sink ObservationSink) *httptest.Server {
	t.Helper()
	auth := NewTokenAuthenticator(map[string]string{"alice-token": "alice", "bob-token": "bob"})
	srv := httptest.NewServer(NewObservationsHandler(auth, sink, nil))
	t.Cleanup(srv.Close)
	return srv
}

func postObservations(t *testing.T, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func validEvent(id string) string {
	return fmt.Sprintf(`{
		"event_id": %q,
		"kind": "ios.location",
		"schema_version": 1,
		"observed_at": "2026-08-29T19:42:00Z",
		"payload": {"latitude": 41.0, "longitude": -87.0, "horizontal_accuracy_m": 24.0}
	}`, id)
}

func decodeResults(t *testing.T, resp *http.Response) []observationResult {
	t.Helper()
	var out observationResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out.Results
}

func TestObservationsRejectsBadAuth(t *testing.T) {
	sink := &fakeObservationSink{}
	srv := newObservationsServer(t, sink)
	body := `{"client_id":"device-1","events":[` + validEvent("evt-1") + `]}`

	for _, tc := range []struct {
		name  string
		token string
	}{
		{"missing token", ""},
		{"wrong token", "not-a-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := postObservations(t, srv.URL, tc.token, body)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			if resp.Header.Get("WWW-Authenticate") == "" {
				t.Error("missing WWW-Authenticate header")
			}
		})
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.ensured) != 0 || len(sink.observations) != 0 {
		t.Error("unauthenticated request reached the sink")
	}
}

func TestObservationsBindsAccountFromToken(t *testing.T) {
	sink := &fakeObservationSink{}
	srv := newObservationsServer(t, sink)
	body := `{"client_id":"device-1","events":[` + validEvent("evt-1") + `]}`

	resp := postObservations(t, srv.URL, "bob-token", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	results := decodeResults(t, resp)
	if len(results) != 1 || results[0].Status != "applied" {
		t.Fatalf("results = %+v", results)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	// The token, never the body, decides the account: bob's token can
	// only write devices under bob.
	if len(sink.ensured) != 1 || sink.ensured[0] != "bob/device-1" {
		t.Errorf("ensured = %v, want [bob/device-1]", sink.ensured)
	}
	obs := sink.observations[0]
	if obs.EventID != "evt-1" || obs.Kind != "ios.location" || obs.SchemaVersion != 1 {
		t.Errorf("observation = %+v", obs)
	}
	want := time.Date(2026, 8, 29, 19, 42, 0, 0, time.UTC)
	if !obs.ObservedAt.Equal(want) {
		t.Errorf("ObservedAt = %v, want %v", obs.ObservedAt, want)
	}
	if !obs.ReceivedAt.After(want) {
		t.Errorf("ReceivedAt = %v, want server receipt time after the observation", obs.ReceivedAt)
	}
}

func TestObservationsBatchValidation(t *testing.T) {
	sink := &fakeObservationSink{}
	srv := newObservationsServer(t, sink)

	manyEvents := make([]string, maxObservationBatch+1)
	for i := range manyEvents {
		manyEvents[i] = validEvent(fmt.Sprintf("evt-%d", i))
	}

	cases := []struct {
		name string
		body string
		want int
	}{
		{"malformed JSON", `{not json`, http.StatusBadRequest},
		{"missing client_id", `{"events":[` + validEvent("e") + `]}`, http.StatusBadRequest},
		{"empty events", `{"client_id":"device-1","events":[]}`, http.StatusBadRequest},
		{"too many events", `{"client_id":"device-1","events":[` + strings.Join(manyEvents, ",") + `]}`, http.StatusBadRequest},
		{"oversize body", `{"client_id":"device-1","events":[{"payload":"` + strings.Repeat("x", maxObservationBodyBytes) + `"}]}`, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postObservations(t, srv.URL, "alice-token", tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

func TestObservationsPerEventValidation(t *testing.T) {
	sink := &fakeObservationSink{}
	srv := newObservationsServer(t, sink)

	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	events := []string{
		validEvent("evt-good"),
		`{"event_id":"","kind":"ios.location","schema_version":1,"observed_at":"2026-08-29T19:42:00Z","payload":{}}`,
		`{"event_id":"evt-badkind","kind":"IOS LOCATION","schema_version":1,"observed_at":"2026-08-29T19:42:00Z","payload":{"a":1}}`,
		`{"event_id":"evt-badver","kind":"ios.location","schema_version":0,"observed_at":"2026-08-29T19:42:00Z","payload":{"a":1}}`,
		`{"event_id":"evt-badtime","kind":"ios.location","schema_version":1,"observed_at":"yesterday-ish","payload":{"a":1}}`,
		`{"event_id":"evt-future","kind":"ios.location","schema_version":1,"observed_at":"` + future + `","payload":{"a":1}}`,
		`{"event_id":"evt-ancient","kind":"ios.location","schema_version":1,"observed_at":"1999-12-31T23:59:00Z","payload":{"a":1}}`,
		`{"event_id":"evt-nopayload","kind":"ios.location","schema_version":1,"observed_at":"2026-08-29T19:42:00Z"}`,
		`{"event_id":"evt-arraypayload","kind":"ios.location","schema_version":1,"observed_at":"2026-08-29T19:42:00Z","payload":[1,2]}`,
		`{"event_id":"evt-armed-withdrawal","kind":"ios.location","schema_version":1,"observed_at":"2026-08-29T19:42:00Z","withdrawn":true,"payload":{"a":1}}`,
		`{"event_id":"evt-withdrawal","kind":"ios.location","schema_version":1,"observed_at":"2026-08-29T19:42:00Z","withdrawn":true}`,
	}
	body := `{"client_id":"device-1","events":[` + strings.Join(events, ",") + `]}`

	resp := postObservations(t, srv.URL, "alice-token", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	results := decodeResults(t, resp)
	if len(results) != len(events) {
		t.Fatalf("got %d results for %d events", len(results), len(events))
	}
	wantStatus := map[string]string{
		"evt-good":             "applied",
		"":                     "invalid",
		"evt-badkind":          "invalid",
		"evt-badver":           "invalid",
		"evt-badtime":          "invalid",
		"evt-future":           "invalid",
		"evt-ancient":          "invalid",
		"evt-nopayload":        "invalid",
		"evt-arraypayload":     "invalid",
		"evt-armed-withdrawal": "invalid",
		"evt-withdrawal":       "applied",
	}
	for _, res := range results {
		if want := wantStatus[res.EventID]; res.Status != want {
			t.Errorf("event %q status = %q (%s), want %q", res.EventID, res.Status, res.Error, want)
		}
		if res.Status == "invalid" && res.Error == "" {
			t.Errorf("event %q invalid without a reason", res.EventID)
		}
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.observations) != 2 {
		t.Fatalf("sink received %d observations, want 2 (valid + withdrawal)", len(sink.observations))
	}
	withdrawal := sink.observations[1]
	if !withdrawal.Withdrawn || len(withdrawal.Payload) != 0 {
		t.Errorf("withdrawal = %+v, want Withdrawn with no payload", withdrawal)
	}
}

func TestObservationsStorageErrorFailsBatch(t *testing.T) {
	sink := &fakeObservationSink{upsertErr: fmt.Errorf("disk on fire")}
	srv := newObservationsServer(t, sink)
	body := `{"client_id":"device-1","events":[` + validEvent("evt-1") + `]}`

	resp := postObservations(t, srv.URL, "alice-token", body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 so the client retries the batch", resp.StatusCode)
	}
}

func TestObservationOutcomesPassThrough(t *testing.T) {
	sink := &fakeObservationSink{outcome: ObservationSuperseded}
	srv := newObservationsServer(t, sink)
	body := `{"client_id":"device-1","events":[` + validEvent("evt-old") + `]}`

	resp := postObservations(t, srv.URL, "alice-token", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	results := decodeResults(t, resp)
	if len(results) != 1 || results[0].Status != "superseded" {
		t.Fatalf("results = %+v, want superseded", results)
	}
}
