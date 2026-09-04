package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/server/web"
)

// companionGatedServer is gatedServer with the companion handlers wired,
// so the companion routes and their legacy aliases are registered.
func companionGatedServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{logger: testAPILogger()}
	s.SetWebServer(web.NewWebServer(web.Config{}))
	s.SetCompanionHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	s.SetCompanionObservationHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	s.SetAuth(config.ListenAuthConfig{
		Tokens:     []config.APIToken{{Label: "alice", Token: "alice-token"}},
		SessionTTL: time.Hour,
	}, map[string]string{"companion-token": "phone"})
	return s
}

// TestCompanionCredentialRefusedOnGatedRoutes is the point of the change:
// the token in a phone's Keychain is not an operator API credential.
//
// Mutation check: delete the principalCompanion branch in authGate.wrap
// and every subtest fails with 200/404 instead of 403.
func TestCompanionCredentialRefusedOnGatedRoutes(t *testing.T) {
	t.Parallel()
	h := gatedServer(t).Handler()

	// Routes an operator reaches and a device must not. Two are
	// destructive, two disclose the operator's own data.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/system"},
		{"GET", "/v1/system/logs"},
		{"GET", "/v1/conversations"},
		{"GET", "/v1/contacts"},
		{"POST", "/v1/sessions/reset"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Authorization", "Bearer companion-token")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("companion on %s %s: got %d, want 403", tc.method, tc.path, rec.Code)
			}
			if body := rec.Body.String(); !strings.Contains(body, "companion credential") {
				t.Errorf("refusal should say why, got %q", body)
			}
		})
	}
}

// TestOperatorCredentialStillReachesGatedRoutes guards the obvious
// regression: scoping companions must not scope operators.
func TestOperatorCredentialStillReachesGatedRoutes(t *testing.T) {
	t.Parallel()
	h := gatedServer(t).Handler()

	req := httptest.NewRequest("GET", "/v1/system", nil)
	req.Header.Set("Authorization", "Bearer alice-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Fatalf("operator token refused on a gated route: %d", rec.Code)
	}
}

// TestCompanionCredentialServesItsOwnSurface proves the scoping does not
// lock a companion out of the routes it exists to call.
func TestCompanionCredentialServesItsOwnSurface(t *testing.T) {
	t.Parallel()
	h := companionGatedServer(t).Handler()

	for _, route := range []string{
		"GET /v1/realtime/ws",
		"POST /v1/companion/observations",
		"GET /v1/companion/ws",
		"GET /v1/platform/ws",
	} {
		t.Run(route, func(t *testing.T) {
			method, path, _ := strings.Cut(route, " ")
			req := httptest.NewRequest(method, path, nil)
			req.Header.Set("Authorization", "Bearer companion-token")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code == http.StatusForbidden {
				t.Fatalf("companion refused on its own surface %s", route)
			}
		})
	}
}

// TestCompanionTokenCannotMintConsoleSession closes the exchange that
// made a device credential an operator's browser session.
//
// Mutation check: restore the `p = Principal{Kind: principalCompanion…}`
// branch in handleAuthLogin and this fails with 200 and a Set-Cookie.
func TestCompanionTokenCannotMintConsoleSession(t *testing.T) {
	t.Parallel()
	h := gatedServer(t).Handler()

	req := httptest.NewRequest("POST", "/v1/auth/login",
		strings.NewReader(`{"token":"companion-token"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("companion sign-in: got %d, want 403", rec.Code)
	}
	if cookies := rec.Result().Cookies(); len(cookies) > 0 {
		t.Fatalf("a session cookie was issued to a companion: %v", cookies)
	}
}

// TestSessionStoreRefusesCompanionPrincipal is the backstop beneath the
// handler, so a future caller of create cannot reopen the hole.
func TestSessionStoreRefusesCompanionPrincipal(t *testing.T) {
	t.Parallel()
	store := newSessionStore(time.Hour)

	if _, err := store.create(Principal{Kind: principalCompanion, Name: "phone"}); err == nil {
		t.Fatal("session store minted a session for a companion principal")
	}
	if _, err := store.create(Principal{Kind: "api_token", Name: "alice"}); err != nil {
		t.Fatalf("session store refused an operator principal: %v", err)
	}
}

// TestCompanionAllowlistMatchesRegisteredRoutes derives the companion
// surface from the route table at runtime — the delta between a server
// with the companion handlers wired and one without — and compares it to
// the allowlist the gate enforces.
//
// It is deliberately not a source scrape. A regex over server.go keys on
// a closed set of handler names, so a third companion handler registered
// later is invisible to it and the two sides drift silently while the
// test stays green.
//
// The delta has one residual dependency of the same kind, and it is
// covered rather than ignored: companionGatedServer has to wire the
// companion handlers, so a third handler it does not know about would be
// absent from both route tables and fall out of the delta unnoticed.
// TestCompanionFixtureWiresEveryCompanionHandler fails in that case, so
// the gap closes at the fixture instead of hiding here.
func TestCompanionAllowlistMatchesRegisteredRoutes(t *testing.T) {
	t.Parallel()

	base := gatedServer(t)
	_ = base.Handler()
	withCompanion := companionGatedServer(t)
	_ = withCompanion.Handler()

	baseRoutes := map[string]bool{}
	for _, r := range base.routes {
		baseRoutes[r] = true
	}
	var registered []string
	for _, r := range withCompanion.routes {
		if !baseRoutes[r] {
			registered = append(registered, r)
		}
	}
	sort.Strings(registered)

	var allowed []string
	for route := range companionRoutes {
		allowed = append(allowed, route)
	}
	sort.Strings(allowed)

	if len(registered) == 0 {
		t.Fatal("no companion routes in the delta; the fixture no longer wires them")
	}
	if strings.Join(registered, "\n") != strings.Join(allowed, "\n") {
		t.Errorf("companion allowlist and registered companion routes disagree\n"+
			"registered:\n  %s\nallowlist:\n  %s\n\n"+
			"A companion route registered without an allowlist entry 403s on the "+
			"companion's own surface; an allowlist entry with no route grants "+
			"nothing but claims to.",
			strings.Join(registered, "\n  "), strings.Join(allowed, "\n  "))
	}
}

// TestCompanionPrincipalResolvesOnItsOwnSurface keeps the coverage the
// auth-gate table lost when its companion row became a refusal: a
// companion token still resolves to the right principal, it is simply
// only useful on the companion surface.
func TestCompanionPrincipalResolvesOnItsOwnSurface(t *testing.T) {
	t.Parallel()

	g := testGate(t)
	var seen Principal
	var present bool
	h := g.wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, present = PrincipalFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/companion/observations", nil)
	req.Header.Set("Authorization", "Bearer companion-token")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !present {
		t.Fatal("companion did not reach its own surface")
	}
	if seen.Kind != principalCompanion || seen.Name != "phone" {
		t.Fatalf("principal = %+v, want %s/phone", seen, principalCompanion)
	}
}

// TestCompanionFixtureWiresEveryCompanionHandler closes the one way the
// route-table delta can still drift.
//
// The delta is only as complete as the fixture that produces it: a
// companion handler companionGatedServer does not wire registers no
// routes, so its surface is missing from both sides of the comparison and
// TestCompanionAllowlistMatchesRegisteredRoutes stays green while a real
// companion route sits outside the allowlist and 403s in production.
//
// Reflection rather than a hand-kept list, for the same reason the delta
// beats a regex: a companion handler field added to Server is found here
// whether or not anyone remembered this test existed.
func TestCompanionFixtureWiresEveryCompanionHandler(t *testing.T) {
	t.Parallel()

	v := reflect.ValueOf(companionGatedServer(t)).Elem()
	typ := v.Type()

	found := 0
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if !strings.HasPrefix(name, "companion") || !strings.HasSuffix(name, "Handler") {
			continue
		}
		found++
		if v.Field(i).IsNil() {
			t.Errorf("Server.%s is not wired by companionGatedServer, so its routes "+
				"are absent from the delta and the allowlist test cannot see them. "+
				"Wire it in the fixture and add its routes to companionSurfaceRoutes.", name)
		}
	}
	if found == 0 {
		t.Fatal("no companion handler fields found; the naming convention this test " +
			"keys on has changed and the guard is now inert")
	}
}

// failingWriter is a ResponseWriter whose body writes fail, standing in
// for a client that disconnected mid-response.
type failingWriter struct{ header http.Header }

func (f *failingWriter) Header() http.Header {
	if f.header == nil {
		f.header = http.Header{}
	}
	return f.header
}
func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }
func (f *failingWriter) WriteHeader(int)           {}

// TestWriteForbiddenSurvivesAClientDisconnect pins the refusal path
// against a nil-logger panic. writeJSON logs encode failures through a
// logger it does not nil-check, and the gate has none to give it, so the
// 403 path writes its own bytes.
//
// Mutation check: restore `writeJSON(w, …, nil)` and this panics.
func TestWriteForbiddenSurvivesAClientDisconnect(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("writeForbidden panicked on a failed write: %v", r)
		}
	}()
	writeForbidden(&failingWriter{}, "a companion credential may not reach this route")
}
