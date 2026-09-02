// Package listen holds the bounds and guards every inbound HTTP listener
// Thane runs must share: the native API, the Ollama and OpenAI shims, and
// CardDAV. It exists so that a listener cannot be added, or a new
// http.Server constructed, without them. Per-surface read and write
// budgets stay with the surface because streaming needs differ; what
// lives here is everything that has no reason to differ.
package listen

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// ReadHeaderTimeout is a header-specific bound, shorter than any
	// surface's ReadTimeout. ReadTimeout covers the whole request read,
	// headers included, but is sized for bodies that may legitimately
	// take tens of seconds to arrive; a client that trickles headers for
	// that long is not a legitimate client, so headers get their own,
	// tighter clock.
	ReadHeaderTimeout = 10 * time.Second
	// IdleTimeout reclaims keep-alive connections nobody is using.
	IdleTimeout = 120 * time.Second
	// MaxHeaderBytes is generous for real clients and a fraction of the
	// 1 MiB net/http default.
	MaxHeaderBytes = 64 << 10
)

// NewServer builds an http.Server with the shared listener bounds applied
// around the surface's own read and write timeouts. addr may be empty when
// the caller serves on a listener it created itself.
func NewServer(addr string, handler http.Handler, readTimeout, writeTimeout time.Duration) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadTimeout:       readTimeout,
		ReadHeaderTimeout: ReadHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    MaxHeaderBytes,
	}
}

// LimitRequestBody caps every request body at maxBytes before handlers
// decode it. Reads past the cap fail with *http.MaxBytesError, and
// net/http closes the connection rather than draining the excess.
func LimitRequestBody(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// RejectCrossOriginWrites refuses state-changing requests that a browser
// reports as coming from another origin. It closes the blind cross-site
// request forgery path: a POST with a text/plain body is a CORS "simple
// request", so a page on any origin can fire it at a listener without a
// preflight, and the absence of CORS headers only hides the response, it
// does not stop the write.
//
// The guard reads two signals browsers attach and non-browser clients do
// not. Sec-Fetch-Site of cross-site or same-site is refused outright;
// same-site is refused too because sibling hostnames under one
// registrable domain are not the same trust boundary. When an Origin
// header is present, its host must equal the request Host; an opaque
// "null" origin is refused. A request carrying neither header, which is
// every curl, Home Assistant, companion, CardDAV, and reverse-proxy
// client, passes unchanged. Safe methods pass unconditionally because
// they change no state and because the WebSocket upgrade is a GET.
//
// When the native API grows a credentialed CORS allowlist for detached
// UI origins, that allowlist belongs here as the one place an Origin is
// admitted.
func RejectCrossOriginWrites(logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isSafeMethod(r.Method) {
			if reason := crossOriginWriteReason(r); reason != "" {
				logger.Warn("refused cross-origin write",
					"method", r.Method,
					"path", r.URL.Path,
					"host", r.Host,
					"origin", r.Header.Get("Origin"),
					"sec_fetch_site", r.Header.Get("Sec-Fetch-Site"),
					"reason", reason,
				)
				writeJSONError(w, http.StatusForbidden, "cross-origin request refused")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// isSafeMethod reports whether the method is defined as safe by RFC 9110
// §9.2.1, meaning it must not change server state.
func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// crossOriginWriteReason returns a short reason when the request's
// browser-supplied origin signals disagree with the request Host, or ""
// when the request is same-origin or carries no such signals.
func crossOriginWriteReason(r *http.Request) string {
	switch site := r.Header.Get("Sec-Fetch-Site"); site {
	case "cross-site", "same-site":
		return "sec-fetch-site " + site
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return ""
	}
	if origin == "null" {
		return "opaque origin"
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return "unparseable origin"
	}
	if !strings.EqualFold(u.Host, r.Host) {
		return "origin host mismatch"
	}
	return ""
}

// writeJSONError writes a minimal {"error": message} body. It is the shape
// every listener's clients can parse, which matters for a guard shared
// across surfaces with different native error envelopes.
func writeJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	body, err := json.Marshal(map[string]string{"error": message})
	if err != nil {
		body = []byte(`{"error":"internal error"}`)
	}
	_, _ = w.Write(body)
}
