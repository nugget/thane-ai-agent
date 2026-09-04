package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/server/listen"
)

// serveHeaders drives one request and returns the response headers.
func serveHeaders(h http.Handler, req *http.Request) (header http.Header) {
	rec := httptest.NewRecorder()
	defer func() {
		_ = recover()
		header = rec.Header()
	}()
	h.ServeHTTP(rec, req)
	return rec.Header()
}

// TestEveryRouteCarriesSecurityHeaders is the route-table test for this
// slice, and the reason the headers go on the chain rather than on the
// routes. It walks every pattern registered on the native mux — the API,
// the console, and the explorer share it — and proves each answers with
// the common set and a content policy. The companion data plane (#1509)
// is about to add authenticated endpoints here; adding one without
// headers should fail in this test the way adding one without an auth
// decision already fails in TestEveryRouteIsGatedOrDeliberatelyPublic.
func TestEveryRouteCarriesSecurityHeaders(t *testing.T) {
	t.Parallel()
	s := gatedServer(t)
	h := s.Handler()

	for _, pattern := range s.routes {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			t.Fatalf("pattern %q has no method", pattern)
		}
		probePath := strings.NewReplacer("{$}", "", "{file...}", "app.js").Replace(path)
		for strings.Contains(probePath, "{") {
			start := strings.Index(probePath, "{")
			end := strings.Index(probePath, "}")
			probePath = probePath[:start] + "x" + probePath[end+1:]
		}
		t.Run(pattern, func(t *testing.T) {
			req := httptest.NewRequest(method, probePath, strings.NewReader("{}"))
			req.Header.Set("Authorization", "Bearer alice-token")
			got := serveHeaders(h, req)
			for name, want := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"X-Frame-Options":        "DENY",
				"Referrer-Policy":        "no-referrer",
			} {
				if got.Get(name) != want {
					t.Errorf("%s: %s = %q, want %q", pattern, name, got.Get(name), want)
				}
			}
			policy := got.Values("Content-Security-Policy")
			if len(policy) != 1 {
				t.Fatalf("%s: got %d content policies, want exactly 1: %v", pattern, len(policy), policy)
			}
			if !strings.Contains(policy[0], "frame-ancestors 'none'") {
				t.Errorf("%s: policy does not deny framing: %s", pattern, policy[0])
			}
		})
	}
}

// TestSurfacePosturesAreTheRightWayRound pins which policy each part of
// the shared mux ends up with: the console and the explorer replace the
// API posture the chain applied on the way in, and everything else keeps
// it. A route that forgets to choose gets the strictest one.
func TestSurfacePosturesAreTheRightWayRound(t *testing.T) {
	t.Parallel()
	h := gatedServer(t).Handler()

	tests := []struct {
		name    string
		path    string
		want    listen.Posture
		comment string
	}{
		{"console shell", "/", listen.PostureConsole, "the console is a document"},
		{"console asset", "/static/app.js", listen.PostureConsole, "assets share the shell's origin"},
		{"explorer", "/docs", listen.PostureDocuments, "the vendored bundle needs data: images"},
		{"explorer asset", "/docs/scalar.js", listen.PostureDocuments, "same surface, same posture"},
		{"native api", "/v1/version", listen.PostureAPI, "an API response is not a page"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer alice-token")
			got := serveHeaders(h, req).Get("Content-Security-Policy")
			if want := securityPolicyFor(tc.want); got != want {
				t.Fatalf("%s (%s):\n got %s\nwant %s", tc.path, tc.comment, got, want)
			}
		})
	}
}

// TestCompatShimsCarrySecurityHeaders pins the header set onto the two
// foreign-protocol surfaces, which have their own chains rather than
// sharing the native mux.
func TestCompatShimsCarrySecurityHeaders(t *testing.T) {
	t.Parallel()

	shims := map[string]http.Handler{
		"ollama": NewOllamaServer("", 11434, "", nil, nil, slog.Default()).Handler(),
		"openai": NewOpenAIServer("", 8081, "", nil, &Server{}, slog.Default()).Handler(),
	}
	paths := map[string]string{"ollama": "/api/version", "openai": "/health"}

	for surface, handler := range shims {
		got := serveHeaders(handler, httptest.NewRequest(http.MethodGet, paths[surface], nil))
		if got.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", surface)
		}
		if want := securityPolicyFor(listen.PostureAPI); got.Get("Content-Security-Policy") != want {
			t.Errorf("%s: policy = %q, want the API posture %q", surface, got.Get("Content-Security-Policy"), want)
		}
	}
}

// securityPolicyFor reads back the policy a posture sends, so these
// tests assert against the guard's own table rather than a copy of it
// that could drift.
func securityPolicyFor(posture listen.Posture) string {
	rec := httptest.NewRecorder()
	listen.SecurityHeaders(posture, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Header().Get("Content-Security-Policy")
}
