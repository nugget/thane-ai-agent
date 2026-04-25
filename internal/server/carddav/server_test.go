package carddav

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
