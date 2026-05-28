package homeassistant

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_GetStatesMeasuresPayload(t *testing.T) {
	body := `[{"entity_id":"light.kitchen","state":"on"},{"entity_id":"switch.fan","state":"off"}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/states" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", nil)

	var states []State
	n, err := client.getMeasured(context.Background(), "/api/states", &states)
	if err != nil {
		t.Fatalf("getMeasured: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("decoded %d states, want 2", len(states))
	}
	if n != int64(len(body)) {
		t.Errorf("payload bytes = %d, want %d", n, len(body))
	}

	// GetStates decodes the same set (and logs the measured cost).
	got, err := client.GetStates(context.Background())
	if err != nil {
		t.Fatalf("GetStates: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("GetStates returned %d states, want 2", len(got))
	}
}

func TestClient_GetMeasuredZeroOnNoResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", nil)

	// With a nil result the body is drained, not decoded, so no bytes are
	// counted — get() must still succeed for callers that ignore the body.
	n, err := client.getMeasured(context.Background(), "/api/whatever", nil)
	if err != nil {
		t.Fatalf("getMeasured: %v", err)
	}
	if n != 0 {
		t.Errorf("payload bytes = %d, want 0 for nil result", n)
	}
}
