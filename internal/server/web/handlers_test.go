package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/events"
	"github.com/nugget/thane-ai-agent/internal/logging"
	"github.com/nugget/thane-ai-agent/internal/loop"
)

// --- Test Doubles ---

// stubRegistry implements [LoopRegistry] for tests.
type stubRegistry struct {
	statuses []loop.Status
	loops    map[string]*loop.Loop
}

func (r *stubRegistry) Statuses() []loop.Status { return r.statuses }
func (r *stubRegistry) Get(id string) *loop.Loop {
	if r.loops == nil {
		return nil
	}
	return r.loops[id]
}

// stubLogQuerier implements [LogQuerier] for tests.
type stubLogQuerier struct {
	entries []logging.LogEntry
	err     error
}

func (q *stubLogQuerier) Query(_ logging.QueryParams) ([]logging.LogEntry, error) {
	return q.entries, q.err
}

func newTestServer(reg LoopRegistry, lq LogQuerier, bus *events.Bus) *WebServer {
	return NewWebServer(Config{
		LoopRegistry: reg,
		EventBus:     bus,
		LogQuerier:   lq,
	})
}

// --- Tests ---

func TestHandleIndex(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Cognition Engine") {
		t.Error("GET / response does not contain 'Cognition Engine'")
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", ct)
	}
}

func TestHandleStatic_CSS(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/static/style.css", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /static/style.css status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("Content-Type = %q, want text/css", ct)
	}
}

func TestHandleStatic_JS(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/static/app.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /static/app.js status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Errorf("Content-Type = %q, want application/javascript", ct)
	}
}

func TestHandleStatic_Blocked(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/static/secrets.txt", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("blocked extension status = %d, want 404", w.Code)
	}
}

func TestHandleStatic_NotFound(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/static/nonexistent.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("nonexistent file status = %d, want 404", w.Code)
	}
}

func TestHandleLoops(t *testing.T) {
	t.Parallel()

	reg := &stubRegistry{
		statuses: []loop.Status{
			{
				ID:         "loop-1",
				Name:       "metacognitive",
				State:      loop.StateSleeping,
				Iterations: 42,
			},
		},
	}
	srv := newTestServer(reg, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/loops", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/loops status = %d, want 200", resp.StatusCode)
	}

	var statuses []loop.Status
	if err := json.NewDecoder(resp.Body).Decode(&statuses); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	if statuses[0].Name != "metacognitive" {
		t.Errorf("name = %q, want metacognitive", statuses[0].Name)
	}
}

func TestHandleLoops_Empty(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/loops", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	body, _ := io.ReadAll(w.Result().Body)
	// Should return valid JSON (null or empty array), not an error.
	if strings.TrimSpace(string(body)) == "" {
		t.Error("empty response body")
	}
}

func TestHandleLoopLogs_NoQuerier(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/loops/loop-1/logs", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestHandleLoopLogs_NotFound(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, &stubLogQuerier{}, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/loops/nonexistent/logs", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleLoopEvents_Snapshot(t *testing.T) {
	t.Parallel()

	reg := &stubRegistry{
		statuses: []loop.Status{
			{ID: "loop-1", Name: "metacognitive", State: loop.StateSleeping},
		},
	}
	bus := events.New()
	srv := newTestServer(reg, nil, bus)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	// Use a real test server to get a proper streaming response.
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/loops/events")
	if err != nil {
		t.Fatalf("GET /api/loops/events: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	// Read the initial snapshot event. We need to read enough bytes
	// to get the first event. Use a deadline to avoid blocking forever.
	buf := make([]byte, 4096)
	// Set a short read deadline via the response body.
	done := make(chan string, 1)
	go func() {
		n, _ := resp.Body.Read(buf)
		done <- string(buf[:n])
	}()

	select {
	case data := <-done:
		if !strings.Contains(data, "event: snapshot") {
			t.Errorf("first event should be snapshot, got: %s", data)
		}
		if !strings.Contains(data, "metacognitive") {
			t.Errorf("snapshot should contain loop name, got: %s", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for snapshot event")
	}
}
