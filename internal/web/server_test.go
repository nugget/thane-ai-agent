package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/router"
)

// newTestServer creates a WebServer with stub providers for testing.
func newTestServer() *WebServer {
	return NewWebServer(Config{
		StatsFunc: func() StatsSnapshot {
			return StatsSnapshot{
				Build: map[string]string{
					"version":    "test-v1.0.0",
					"git_commit": "abc1234",
					"go_version": "go1.24.0",
				},
			}
		},
		RouterFunc: func() RouterInfo {
			return RouterInfo{
				Stats: router.Stats{
					TotalRequests: 100,
					ModelCounts:   map[string]int64{"test-model": 50},
				},
				Models: []router.Model{
					{
						Name:          "test-model",
						Provider:      "ollama",
						SupportsTools: true,
						Speed:         8,
						Quality:       5,
						CostTier:      0,
						ContextWindow: 8192,
					},
				},
			}
		},
		HealthFunc: func() map[string]HealthStatus {
			return map[string]HealthStatus{
				"home_assistant": {Connected: true, Since: time.Now()},
				"ollama":         {Connected: false, LastError: "connection refused"},
			}
		},
		Logger: slog.Default(),
	})
}

func TestDashboard_FullPage(t *testing.T) {
	ws := newTestServer()
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()

	// Full page should include DOCTYPE, nav, brand name, and build info
	for _, want := range []string{"<!DOCTYPE html>", "<nav", "Thane", "test-v1.0.0", "abc1234"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET / response missing %q", want)
		}
	}
}

func TestDashboard_HtmxPartial(t *testing.T) {
	ws := newTestServer()
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET / (htmx) status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()

	// Partial should NOT include DOCTYPE or nav
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("htmx partial should not contain <!DOCTYPE html>")
	}
	if strings.Contains(body, "<nav") {
		t.Error("htmx partial should not contain <nav>")
	}

	// But should contain dashboard content
	if !strings.Contains(body, "test-v1.0.0") {
		t.Error("htmx partial should contain version info")
	}
}

func TestDashboard_SubpathNotFound(t *testing.T) {
	ws := newTestServer()
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/nonexistent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GET /nonexistent status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestStaticFiles(t *testing.T) {
	ws := newTestServer()
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/static/htmx.min.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /static/htmx.min.js status = %d, want %d", w.Code, http.StatusOK)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want javascript", ct)
	}
}

func TestStaticCSS(t *testing.T) {
	ws := newTestServer()
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/static/style.css", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /static/style.css status = %d, want %d", w.Code, http.StatusOK)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "css") {
		t.Errorf("Content-Type = %q, want css", ct)
	}
}

func TestChat_RendersInLayout(t *testing.T) {
	ws := newTestServer()
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/chat", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /chat status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()

	// Chat page should be rendered inside the shared layout with nav
	for _, want := range []string{"<!DOCTYPE html>", "<nav", "Thane", "Message Thane..."} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /chat response missing %q", want)
		}
	}

	// Chat nav link should be active
	if !strings.Contains(body, `class="nav-link active"`) {
		t.Error("GET /chat should have an active nav link")
	}
}

func TestStatic_BlocksChatHTML(t *testing.T) {
	ws := newTestServer()
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	// index.html exists in static/ but should not be served via /static/
	for _, path := range []string{"/static/index.html", "/static/manifest.json", "/static/"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want %d", path, w.Code, http.StatusNotFound)
		}
	}
}

func TestDashboard_NilProviders(t *testing.T) {
	// WebServer with nil function providers should not panic.
	ws := NewWebServer(Config{Logger: slog.Default()})
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET / (nil providers) status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestBrandName_Custom(t *testing.T) {
	ws := NewWebServer(Config{
		BrandName: "TestBot",
		Logger:    slog.Default(),
	})
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	// Dashboard should use custom brand name
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "TestBot") {
		t.Error("dashboard should contain custom brand name")
	}

	// Chat should also use custom brand name
	req = httptest.NewRequest("GET", "/chat", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	body = w.Body.String()
	if !strings.Contains(body, "TestBot") {
		t.Error("chat page should contain custom brand name")
	}
	if !strings.Contains(body, "Message TestBot...") {
		t.Error("chat placeholder should contain custom brand name")
	}
}
