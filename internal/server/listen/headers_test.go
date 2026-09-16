package listen

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// commonHeaders are sent by every posture; a surface cannot opt out of
// them by choosing one posture over another.
var commonHeaders = map[string]string{
	"X-Content-Type-Options": "nosniff",
	"X-Frame-Options":        "DENY",
	"Referrer-Policy":        "no-referrer",
	"Permissions-Policy":     "geolocation=(), camera=(), microphone=()",
}

func TestSecurityHeadersCommonSet(t *testing.T) {
	t.Parallel()

	for _, posture := range []Posture{PostureAPI, PostureConsole, PostureDocuments} {
		rec := httptest.NewRecorder()
		SecurityHeaders(posture, comparableHandler{}).
			ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		for name, want := range commonHeaders {
			if got := rec.Header().Get(name); got != want {
				t.Errorf("posture %d: %s = %q, want %q", posture, name, got, want)
			}
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != contentPolicy[posture] {
			t.Errorf("posture %d sent %q, want its own policy %q", posture, got, contentPolicy[posture])
		}
		// HSTS is the front door's to send; a surface asserting it would
		// be promising a transport it does not terminate.
		if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
			t.Errorf("posture %d sent HSTS %q; that belongs to the front door", posture, got)
		}
	}
}

// TestConsolePolicyLoosensNothing is the test that keeps the console's
// policy honest. The console loads no external origin, evaluates no
// strings, and embeds no images, so every directive should name 'self'
// or 'none'. If a future asset needs more, the choice should be to
// change the asset, and this failing is where that conversation starts.
func TestConsolePolicyLoosensNothing(t *testing.T) {
	t.Parallel()

	policy := contentPolicy[PostureConsole]
	for _, banned := range []string{"'unsafe-inline'", "'unsafe-eval'", "'unsafe-hashes'", "data:", "blob:", "*", "http:", "https:"} {
		if strings.Contains(policy, banned) {
			t.Errorf("console policy contains %s: %s", banned, policy)
		}
	}
	for _, required := range []string{
		"default-src 'none'",
		"script-src 'self'",
		"style-src 'self'",
		"connect-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
	} {
		if !strings.Contains(policy, required) {
			t.Errorf("console policy missing %q: %s", required, policy)
		}
	}
}

// TestAPIPolicyIsForDataNotDocuments pins that an API response says it
// is not a page. This is what keeps a stored companion materialization
// (#1509) — opaque client-authored JSON served back from the origin the
// console's session cookie lives on — from being treated as anything
// executable.
func TestAPIPolicyIsForDataNotDocuments(t *testing.T) {
	t.Parallel()

	policy := contentPolicy[PostureAPI]
	if !strings.Contains(policy, "default-src 'none'") {
		t.Fatalf("api policy must deny everything by default: %s", policy)
	}
	for _, banned := range []string{"'unsafe-inline'", "'unsafe-eval'", "data:", "'self'"} {
		if strings.Contains(policy, banned) {
			t.Errorf("api policy contains %s; an API response loads nothing: %s", banned, policy)
		}
	}
}

// TestDocumentsPolicyLoosensOnlyWhatTheBundleNeeds pins the blast radius
// of the one surface that carries a vendored asset: it may relax images
// and styles for Scalar, and nothing else, and never for scripts.
func TestDocumentsPolicyLoosensOnlyWhatTheBundleNeeds(t *testing.T) {
	t.Parallel()

	policy := contentPolicy[PostureDocuments]
	if !strings.Contains(policy, "script-src 'self'") || strings.Contains(policy, "'unsafe-eval'") {
		t.Errorf("documents policy must keep scripts strict: %s", policy)
	}
	for _, directive := range strings.Split(policy, "; ") {
		name, value, _ := strings.Cut(directive, " ")
		switch name {
		case "img-src", "font-src":
			if strings.ReplaceAll(value, "data:", "") != "'self' " && value != "'self' data:" {
				t.Errorf("%s = %q, want 'self' plus data: only", name, value)
			}
		case "style-src":
			if value != "'self' 'unsafe-inline'" {
				t.Errorf("style-src = %q, want 'self' 'unsafe-inline' for the bundle's runtime stylesheets", value)
			}
		default:
			if strings.Contains(value, "data:") || strings.Contains(value, "'unsafe-inline'") {
				t.Errorf("%s is loosened but is not a bundle requirement: %s", name, directive)
			}
		}
	}
}

// TestUnknownPostureFailsClosed pins the fallback. Posture is exported
// and meant to grow, so the interesting case is not a posture Thane has
// today but the one added tomorrow with the constant written and the map
// entry forgotten. An empty policy is the loosest thing this middleware
// could send, on the surface most likely to be new and unexamined, so an
// unrecognised posture answers with the API policy instead.
func TestUnknownPostureFailsClosed(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	SecurityHeaders(Posture(999), comparableHandler{}).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("Content-Security-Policy"); got != contentPolicy[PostureAPI] {
		t.Errorf("unknown posture sent %q, want the API policy %q", got, contentPolicy[PostureAPI])
	}
	for name, want := range commonHeaders {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("unknown posture: %s = %q, want %q", name, got, want)
		}
	}
}

// TestEveryPostureHasItsOwnPolicy keeps the fallback from being a place
// postures quietly live. Falling back is the safe behaviour for a
// mistake, not a way to declare a posture without deciding what it
// serves, so every named posture carries its own entry.
func TestEveryPostureHasItsOwnPolicy(t *testing.T) {
	t.Parallel()

	for _, posture := range []Posture{PostureAPI, PostureConsole, PostureDocuments} {
		if _, ok := contentPolicy[posture]; !ok {
			t.Errorf("posture %d has no policy of its own", posture)
		}
	}
}

// TestSecurityHeadersAreOverridable pins the composition the native mux
// depends on: headers are set before the handler runs, so a sub-tree
// that knows its own posture replaces them rather than appending.
func TestSecurityHeadersAreOverridable(t *testing.T) {
	t.Parallel()

	inner := SecurityHeaders(PostureConsole, comparableHandler{})
	rec := httptest.NewRecorder()
	SecurityHeaders(PostureAPI, inner).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Values("Content-Security-Policy"); len(got) != 1 {
		t.Fatalf("got %d content policies, want exactly 1: %v", len(got), got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != contentPolicy[PostureConsole] {
		t.Fatalf("inner posture did not win: %s", got)
	}
}
