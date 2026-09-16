package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

// homeLAN is the prefix the wiring tests admit from; the refused peers
// below sit outside it.
var homeLAN = []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}

// TestOllamaServerRestrictsSources pins the guard onto the Ollama shim's
// handler chain: this is the surface whose callers cannot present a
// credential, so the address list is the whole of its admission policy.
func TestOllamaServerRestrictsSources(t *testing.T) {
	t.Parallel()

	handler := NewOllamaServer("", 11434, "", homeLAN, nil, slog.Default()).Handler()

	t.Run("peer outside the list is refused", func(t *testing.T) {
		t.Parallel()
		rec := serveFrom(handler, http.MethodGet, "/api/version", "203.0.113.9:52000", "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("peer inside the list reaches the routes", func(t *testing.T) {
		t.Parallel()
		rec := serveFrom(handler, http.MethodGet, "/api/version", "192.168.1.44:52000", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("an unset list leaves the surface open", func(t *testing.T) {
		t.Parallel()
		open := NewOllamaServer("", 11434, "", nil, nil, slog.Default()).Handler()
		rec := serveFrom(open, http.MethodGet, "/api/version", "203.0.113.9:52000", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
	})
}

// TestOpenAIServerRestrictsSourcesBeforeAuth pins both the guard's
// presence on the OpenAI shim and its position: a caller outside the list
// is refused with 403 without its bearer key ever being compared, so a
// correct key from a disallowed host gets no different an answer than a
// wrong one.
func TestOpenAIServerRestrictsSourcesBeforeAuth(t *testing.T) {
	t.Parallel()

	handler := NewOpenAIServer("", 8081, "sekrit", homeLAN, &Server{}, slog.Default()).Handler()

	tests := []struct {
		name     string
		remote   string
		auth     string
		wantCode int
	}{
		{"disallowed peer without a key", "203.0.113.9:52000", "", http.StatusForbidden},
		{"disallowed peer with the correct key", "203.0.113.9:52000", "Bearer sekrit", http.StatusForbidden},
		{"allowed peer without a key still needs one", "192.168.1.44:52000", "", http.StatusUnauthorized},
		{"allowed peer with the correct key is served", "192.168.1.44:52000", "Bearer sekrit", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := serveFrom(handler, http.MethodGet, "/health", tc.remote, tc.auth)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

// serveFrom drives one request at handler as though it arrived from
// remote, which is what net/http fills RemoteAddr with on a real socket.
func serveFrom(handler http.Handler, method, path, remote, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remote
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
