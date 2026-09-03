package carddav

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/server/listen"
)

func TestServer_AuthRequired(t *testing.T) {
	b := newTestBackend(t)
	s := NewServer(nil, "user", "pass", b, b.logger)
	s.handler = s.buildHandler()

	req := httptest.NewRequest("PROPFIND", "/carddav/", nil)
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if got := rr.Header().Get("WWW-Authenticate"); got == "" {
		t.Error("expected WWW-Authenticate header")
	}
}

func TestServer_AuthWrongCredentials(t *testing.T) {
	b := newTestBackend(t)
	s := NewServer(nil, "user", "pass", b, b.logger)
	s.handler = s.buildHandler()

	req := httptest.NewRequest("PROPFIND", "/carddav/", nil)
	req.SetBasicAuth("user", "wrong")
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestServer_AuthCorrectCredentials(t *testing.T) {
	b := newTestBackend(t)
	s := NewServer(nil, "user", "pass", b, b.logger)
	s.handler = s.buildHandler()

	req := httptest.NewRequest("PROPFIND", "/carddav/", nil)
	req.SetBasicAuth("user", "pass")
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)

	// Should not be 401 — the actual response code depends on the
	// CardDAV handler (likely 207 Multi-Status or similar).
	if rr.Code == http.StatusUnauthorized {
		t.Errorf("authenticated request returned 401")
	}
}

func TestServer_WellKnownRedirect(t *testing.T) {
	b := newTestBackend(t)
	s := NewServer(nil, "user", "pass", b, b.logger)
	s.handler = s.buildHandler()

	req := httptest.NewRequest("GET", "/.well-known/carddav", nil)
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusMovedPermanently)
	}
	if loc := rr.Header().Get("Location"); loc != "/carddav/" {
		t.Errorf("Location = %q, want %q", loc, "/carddav/")
	}
}

func TestServer_WellKnownNoAuth(t *testing.T) {
	b := newTestBackend(t)
	s := NewServer(nil, "user", "pass", b, b.logger)
	s.handler = s.buildHandler()

	// .well-known should work without credentials.
	req := httptest.NewRequest("GET", "/.well-known/carddav", nil)
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)

	if rr.Code == http.StatusUnauthorized {
		t.Error("well-known redirect should not require auth")
	}
}

// TestServer_SecurityHeadersOnEveryResponse covers the two responses
// CardDAV returns before authentication: the 401 challenge and the
// unauthenticated .well-known discovery redirect. Both leave the server
// without touching the CardDAV handler, so they are the responses a
// chain that put the headers behind auth would silently omit them from.
//
// The expected headers are taken from the API posture itself rather than
// written out, so this test asserts CardDAV carries that posture and
// cannot drift from what listen defines it to be.
func TestServer_SecurityHeadersOnEveryResponse(t *testing.T) {
	b := newTestBackend(t)
	s := NewServer(nil, "user", "pass", b, b.logger)
	s.handler = s.buildHandler()

	posture := httptest.NewRecorder()
	listen.SecurityHeaders(listen.PostureAPI, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(posture, httptest.NewRequest("GET", "/", nil))

	tests := []struct {
		name  string
		build func() *http.Request
		want  int
	}{
		{
			name:  "unauthenticated challenge",
			build: func() *http.Request { return httptest.NewRequest("PROPFIND", "/carddav/", nil) },
			want:  http.StatusUnauthorized,
		},
		{
			name:  "discovery redirect",
			build: func() *http.Request { return httptest.NewRequest("GET", "/.well-known/carddav", nil) },
			want:  http.StatusMovedPermanently,
		},
		{
			name: "authenticated request",
			build: func() *http.Request {
				req := httptest.NewRequest("PROPFIND", "/carddav/", nil)
				req.SetBasicAuth("user", "pass")
				return req
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, tt.build())

			if tt.want != 0 && rr.Code != tt.want {
				t.Errorf("status = %d, want %d", rr.Code, tt.want)
			}
			for name, want := range posture.Header() {
				if got := rr.Header().Get(name); got != want[0] {
					t.Errorf("%s = %q, want %q", name, got, want[0])
				}
			}
		})
	}
}

// TestServer_EveryResponseIsLogged covers the responses this server
// produces on its own rather than by serving contacts: the 401 challenge
// and the unauthenticated .well-known discovery redirect. Both return
// from inside withAuth, so while auth sat outside logging neither
// appeared in the access log — a run of guessed credentials against
// /carddav/ was invisible on the one surface that answers to a password.
func TestServer_EveryResponseIsLogged(t *testing.T) {
	tests := []struct {
		name       string
		build      func() *http.Request
		wantMethod string
		wantPath   string
		wantStatus int
	}{
		{
			name:       "unauthenticated challenge",
			build:      func() *http.Request { return httptest.NewRequest("PROPFIND", "/carddav/", nil) },
			wantMethod: "PROPFIND",
			wantPath:   "/carddav/",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "wrong credentials",
			build: func() *http.Request {
				req := httptest.NewRequest("PROPFIND", "/carddav/", nil)
				req.SetBasicAuth("user", "wrong")
				return req
			},
			wantMethod: "PROPFIND",
			wantPath:   "/carddav/",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "discovery redirect",
			build:      func() *http.Request { return httptest.NewRequest("GET", "/.well-known/carddav", nil) },
			wantMethod: "GET",
			wantPath:   "/.well-known/carddav",
			wantStatus: http.StatusMovedPermanently,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newTestBackend(t)

			var buf bytes.Buffer
			s := NewServer(nil, "user", "pass", b, slog.New(slog.NewJSONHandler(&buf, nil)))
			s.handler = s.buildHandler()
			s.handler.ServeHTTP(httptest.NewRecorder(), tt.build())

			var line struct {
				Kind   string `json:"kind"`
				Server string `json:"server"`
				Method string `json:"method"`
				Path   string `json:"path"`
				Status int    `json:"status"`
				Remote string `json:"remote"`
			}
			if err := json.NewDecoder(&buf).Decode(&line); err != nil {
				t.Fatalf("no access-log line for this response: %v", err)
			}

			if line.Kind != logging.KindHTTPAccess || line.Server != "carddav" {
				t.Errorf("kind/server = %q/%q, want %q/carddav", line.Kind, line.Server, logging.KindHTTPAccess)
			}
			if line.Method != tt.wantMethod || line.Path != tt.wantPath {
				t.Errorf("method/path = %q/%q, want %q/%q", line.Method, line.Path, tt.wantMethod, tt.wantPath)
			}
			if line.Status != tt.wantStatus {
				t.Errorf("status = %d, want %d", line.Status, tt.wantStatus)
			}
			// The peer is what makes a run of failures actionable
			// rather than merely visible.
			if line.Remote == "" {
				t.Error("access-log line names no peer")
			}
		})
	}
}

func TestServer_StartAndShutdown(t *testing.T) {
	b := newTestBackend(t)

	// Use port 0 to get a random available port.
	s := NewServer([]string{"127.0.0.1:0"}, "user", "pass", b, b.logger)

	ctx := t.Context()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify a listener is bound.
	s.mu.Lock()
	count := len(s.servers)
	s.mu.Unlock()
	if count != 1 {
		t.Errorf("expected 1 bound server, got %d", count)
	}

	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestServer_PartialBindFailure(t *testing.T) {
	b := newTestBackend(t)

	// One valid address (port 0) and one invalid.
	s := NewServer(
		[]string{"127.0.0.1:0", "192.0.2.1:99999"},
		"user", "pass", b, b.logger,
	)

	ctx := t.Context()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(ctx)

	s.mu.Lock()
	boundCount := len(s.servers)
	unboundCount := len(s.unbound)
	s.mu.Unlock()

	if boundCount != 1 {
		t.Errorf("expected 1 bound server, got %d", boundCount)
	}
	if unboundCount != 1 {
		t.Errorf("expected 1 unbound address, got %d", unboundCount)
	}
}

func TestServer_AllBindFailure(t *testing.T) {
	b := newTestBackend(t)

	s := NewServer(
		[]string{"192.0.2.1:99999", "192.0.2.2:99999"},
		"user", "pass", b, b.logger,
	)

	err := s.Start(t.Context())
	if err == nil {
		t.Error("expected error when no addresses can bind")
		s.Shutdown(t.Context())
	}
}
