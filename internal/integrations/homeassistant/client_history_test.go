package homeassistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClient_GetStateHistory(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			[
				{
					"entity_id":"sensor.temp",
					"state":"70.1",
					"last_changed":"2025-01-15T10:00:00Z",
					"last_updated":"2025-01-15T10:00:00Z"
				},
				{
					"entity_id":"sensor.temp",
					"state":"71.4",
					"last_changed":"2025-01-15T11:00:00Z",
					"last_updated":"2025-01-15T11:00:00Z"
				}
			]
		]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", nil)
	start := time.Date(2025, 1, 15, 9, 0, 0, 0, time.UTC)
	end := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	states, err := client.GetStateHistory(context.Background(), "sensor.temp", start, end)
	if err != nil {
		t.Fatalf("GetStateHistory: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("len(states) = %d, want 2", len(states))
	}
	if states[0].State != "70.1" || states[1].State != "71.4" {
		t.Fatalf("states = %#v", states)
	}
	wantPath := "/api/history/period/2025-01-15T09:00:00Z?end_time=2025-01-15T12%3A00%3A00Z&filter_entity_id=sensor.temp&no_attributes=1&significant_changes_only=0"
	if capturedPath != wantPath {
		t.Fatalf("path = %q, want %q", capturedPath, wantPath)
	}
}

func TestClient_GetWeatherForecasts(t *testing.T) {
	var capturedPath string
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.RequestURI()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &capturedBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"changed_states": [],
			"service_response": {
				"weather.home": {
					"forecast": [
						{"datetime":"2026-04-30T18:00:00Z","condition":"rainy","temperature":78}
					]
				}
			}
		}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", nil)
	forecast, err := client.GetWeatherForecasts(context.Background(), "weather.home", "hourly")
	if err != nil {
		t.Fatalf("GetWeatherForecasts: %v", err)
	}
	if capturedPath != "/api/services/weather/get_forecasts?return_response" {
		t.Fatalf("path = %q, want return_response service path", capturedPath)
	}
	if capturedBody["entity_id"] != "weather.home" {
		t.Fatalf("entity_id = %v, want weather.home", capturedBody["entity_id"])
	}
	if capturedBody["type"] != "hourly" {
		t.Fatalf("type = %v, want hourly", capturedBody["type"])
	}
	if len(forecast) != 1 {
		t.Fatalf("len(forecast) = %d, want 1", len(forecast))
	}
	if forecast[0]["condition"] != "rainy" {
		t.Fatalf("forecast condition = %v, want rainy", forecast[0]["condition"])
	}
}
