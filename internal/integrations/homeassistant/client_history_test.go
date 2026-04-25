package homeassistant

import (
	"context"
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
