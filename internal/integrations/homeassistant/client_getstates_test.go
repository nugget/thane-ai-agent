package homeassistant

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureHandler collects slog records so a test can assert the log
// contract a feature promises (message + attributes).
type captureHandler struct {
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

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

func TestClient_GetStatesLogsCost(t *testing.T) {
	body := `[{"entity_id":"light.kitchen","state":"on"},{"entity_id":"switch.fan","state":"off"}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	h := &captureHandler{}
	client := NewClient(server.URL, "token", slog.New(h))

	if _, err := client.GetStates(context.Background()); err != nil {
		t.Fatalf("GetStates: %v", err)
	}

	var rec *slog.Record
	for i := range h.records {
		if strings.Contains(h.records[i].Message, "get_states") {
			rec = &h.records[i]
			break
		}
	}
	if rec == nil {
		t.Fatal("GetStates emitted no get_states cost log record")
	}

	attrs := map[string]slog.Value{}
	rec.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value
		return true
	})
	for _, key := range []string{"duration", "payload_bytes", "entity_count"} {
		if _, ok := attrs[key]; !ok {
			t.Errorf("cost log missing attr %q", key)
		}
	}
	if got := attrs["entity_count"].Int64(); got != 2 {
		t.Errorf("entity_count = %d, want 2", got)
	}
	if got := attrs["payload_bytes"].Int64(); got != int64(len(body)) {
		t.Errorf("payload_bytes = %d, want %d", got, len(body))
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
