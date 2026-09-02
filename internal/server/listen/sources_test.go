package listen

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func mustPrefixes(t *testing.T, entries ...string) []netip.Prefix {
	t.Helper()
	prefixes := make([]netip.Prefix, 0, len(entries))
	for _, entry := range entries {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			t.Fatalf("ParsePrefix(%q): %v", entry, err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

func TestRestrictSources(t *testing.T) {
	t.Parallel()

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name     string
		allowed  []string
		remote   string
		wantCode int
	}{
		{"empty list leaves the surface open", nil, "203.0.113.9:52000", http.StatusOK},
		{"peer inside the prefix admitted", []string{"192.168.1.0/24"}, "192.168.1.44:52000", http.StatusOK},
		{"peer outside every prefix refused", []string{"192.168.1.0/24"}, "192.168.2.44:52000", http.StatusForbidden},
		{"second prefix admits", []string{"192.168.1.0/24", "10.0.0.0/8"}, "10.9.9.9:52000", http.StatusOK},
		{"single-host prefix admits exactly that host", []string{"192.168.1.44/32"}, "192.168.1.44:52000", http.StatusOK},
		{"single-host prefix refuses the neighbour", []string{"192.168.1.44/32"}, "192.168.1.45:52000", http.StatusForbidden},
		{"loopback prefix admits loopback", []string{"127.0.0.0/8"}, "127.0.0.1:52000", http.StatusOK},
		{"IPv4-mapped IPv6 peer matches an IPv4 prefix", []string{"192.168.1.0/24"}, "[::ffff:192.168.1.44]:52000", http.StatusOK},
		{"IPv4-mapped IPv6 peer outside the prefix refused", []string{"192.168.1.0/24"}, "[::ffff:192.168.2.44]:52000", http.StatusForbidden},
		{"IPv6 peer matches an IPv6 prefix", []string{"2001:db8::/32"}, "[2001:db8::5]:52000", http.StatusOK},
		{"IPv6 peer does not match an IPv4 prefix", []string{"0.0.0.0/0"}, "[2001:db8::5]:52000", http.StatusForbidden},
		{"link-local peer with a zone matches its prefix", []string{"fe80::/10"}, "[fe80::1%en0]:52000", http.StatusOK},
		{"bare address without a port is still read", []string{"192.168.1.0/24"}, "192.168.1.44", http.StatusOK},
		{"unparseable peer refused", []string{"192.168.1.0/24"}, "/var/run/thane.sock", http.StatusForbidden},
		{"empty peer refused", []string{"192.168.1.0/24"}, "", http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
			req.RemoteAddr = tc.remote
			rec := httptest.NewRecorder()
			RestrictSources(nil, "ollama", mustPrefixes(t, tc.allowed...), next).ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode != http.StatusForbidden {
				return
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", ct)
			}
			if body := rec.Body.String(); body != `{"error":"source address not allowed"}` {
				t.Fatalf("body = %s, want the shared JSON error envelope", body)
			}
		})
	}
}

// TestRestrictSourcesGuardsSafeMethods pins that the source policy is not
// the cross-origin guard: a GET is state-changing enough on a surface
// that runs the agent loop, and every method is subject to the list.
func TestRestrictSourcesGuardsSafeMethods(t *testing.T) {
	t.Parallel()

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPost} {
		req := httptest.NewRequest(method, "/api/tags", nil)
		req.RemoteAddr = "203.0.113.9:52000"
		rec := httptest.NewRecorder()
		RestrictSources(nil, "ollama", mustPrefixes(t, "192.168.1.0/24"), next).ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d, want 403", method, rec.Code)
		}
	}
}

// TestRestrictSourcesEmptyListSkipsTheWrapper pins the zero-cost default:
// with no prefixes configured the guard returns the next handler itself,
// so an open surface carries no per-request work.
func TestRestrictSourcesEmptyListSkipsTheWrapper(t *testing.T) {
	t.Parallel()

	var next comparableHandler
	if got := RestrictSources(nil, "ollama", nil, next); got != http.Handler(next) {
		t.Fatalf("RestrictSources with no prefixes wrapped the handler")
	}
}

// comparableHandler is an http.Handler that == compares, which an
// http.HandlerFunc does not.
type comparableHandler struct{}

func (comparableHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// TestRestrictSourcesLogsTheMatchedAddress pins what a refusal writes: an
// operator reading the log must be able to paste peer into
// allowed_sources unedited, so it carries the address the guard matched —
// no port, unmapped, no zone — with the raw socket string beside it.
func TestRestrictSourcesLogsTheMatchedAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		remote         string
		wantPeer       string
		wantRemoteAddr string
	}{
		{"port stripped", "203.0.113.9:52000", "203.0.113.9", "203.0.113.9:52000"},
		{"IPv4-mapped peer logged as the IPv4 host", "[::ffff:203.0.113.9]:52000", "203.0.113.9", "[::ffff:203.0.113.9]:52000"},
		{"zone stripped", "[fe80::1%en0]:52000", "fe80::1", "[fe80::1%en0]:52000"},
		{"unparseable peer says so and keeps the raw string", "/var/run/thane.sock", "invalid IP", "/var/run/thane.sock"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

			req := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
			req.RemoteAddr = tc.remote
			RestrictSources(logger, "ollama", mustPrefixes(t, "192.168.1.0/24"), comparableHandler{}).
				ServeHTTP(httptest.NewRecorder(), req)

			var line struct {
				Level      string `json:"level"`
				Surface    string `json:"surface"`
				Peer       string `json:"peer"`
				RemoteAddr string `json:"remote_addr"`
			}
			if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
				t.Fatalf("log line %q: %v", buf.String(), err)
			}
			if line.Level != "WARN" || line.Surface != "ollama" {
				t.Fatalf("level/surface = %q/%q, want WARN/ollama", line.Level, line.Surface)
			}
			if line.Peer != tc.wantPeer || line.RemoteAddr != tc.wantRemoteAddr {
				t.Fatalf("peer/remote_addr = %q/%q, want %q/%q", line.Peer, line.RemoteAddr, tc.wantPeer, tc.wantRemoteAddr)
			}
		})
	}
}
