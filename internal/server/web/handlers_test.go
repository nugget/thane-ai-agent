package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer() *WebServer {
	return NewWebServer(Config{})
}

func TestHandleIndex(t *testing.T) {
	t.Parallel()

	srv := newTestServer()
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

	srv := newTestServer()
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

	srv := newTestServer()
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

	srv := newTestServer()
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

	srv := newTestServer()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/static/nonexistent.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("nonexistent file status = %d, want 404", w.Code)
	}
}

func TestHandleIndex_RetiredPathIs404(t *testing.T) {
	t.Parallel()

	srv := newTestServer()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	// Retired /api/* URLs must 404, not return a 200 dashboard shell.
	req := httptest.NewRequest("GET", "/api/system", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GET /api/system status = %d, want 404 (not the dashboard shell)", w.Code)
	}
}

func TestHandleStatic_NestedModules(t *testing.T) {
	t.Parallel()

	srv := newTestServer()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	// The SPA boot imports nested ES modules (main.js: ./views/*, ./data/*).
	// Guard that the {file...} wildcard keeps serving subdirectory assets, so a
	// future change to the path handling can't silently break console boot.
	for _, path := range []string{
		"/static/views/placeholder.js",
		"/static/data/viewState.js",
	} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		resp := w.Result()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
			t.Errorf("GET %s Content-Type = %q, want application/javascript", path, ct)
		}
	}
}

func TestHandleStatic_ETagRevalidation(t *testing.T) {
	t.Parallel()

	srv := newTestServer()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	// First request: a 200 carrying a strong ETag and a revalidation directive.
	req := httptest.NewRequest("GET", "/static/app.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /static/app.js status = %d, want 200", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("GET /static/app.js missing ETag header")
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}

	// Conditional re-request with the same validator: a 304 with an empty body.
	req2 := httptest.NewRequest("GET", "/static/app.js", nil)
	req2.Header.Set("If-None-Match", etag)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	resp2 := w2.Result()
	if resp2.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional GET status = %d, want 304", resp2.StatusCode)
	}
	body, _ := io.ReadAll(resp2.Body)
	if len(body) != 0 {
		t.Errorf("304 response body = %d bytes, want empty", len(body))
	}
}
