package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/server/web"
)

func testGate(t *testing.T) *authGate {
	t.Helper()
	cfg := config.ListenAuthConfig{
		Tokens:     []config.APIToken{{Label: "alice", Token: "alice-token"}, {Label: "bob-cli", Token: "bob-token"}},
		SessionTTL: time.Hour,
	}
	return newAuthGate(cfg, map[string]string{"companion-token": "phone"}, testAPILogger())
}

// gatedServer builds a Server with the gate configured and the full route
// table, the way production does, so tests exercise the real chain.
func gatedServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{logger: testAPILogger()}
	s.SetWebServer(web.NewWebServer(web.Config{}))
	s.SetAuth(config.ListenAuthConfig{
		Tokens:     []config.APIToken{{Label: "alice", Token: "alice-token"}},
		SessionTTL: time.Hour,
	}, map[string]string{"companion-token": "phone"})
	return s
}

func TestAuthGateNilIsOpen(t *testing.T) {
	t.Parallel()
	var g *authGate
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	rec := httptest.NewRecorder()
	g.wrap(next).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/loop-definitions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("nil gate refused: %d", rec.Code)
	}
	if newAuthGate(config.ListenAuthConfig{}, nil, nil) != nil {
		t.Fatal("gate built with no tokens configured")
	}
}

func TestAuthGateCredentials(t *testing.T) {
	t.Parallel()
	g := testGate(t)
	var seen Principal
	var present bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, present = PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := g.wrap(next)

	tests := []struct {
		name     string
		header   string
		wantCode int
		wantKind string
		wantName string
	}{
		{"no credential refused", "", http.StatusUnauthorized, "", ""},
		{"operator token accepted", "Bearer alice-token", http.StatusOK, "api_token", "alice"},
		{"second operator token accepted", "Bearer bob-token", http.StatusOK, "api_token", "bob-cli"},
		{"companion token accepted as account", "Bearer companion-token", http.StatusOK, "companion", "phone"},
		{"wrong token refused", "Bearer nope", http.StatusUnauthorized, "", ""},
		{"prefix of token refused", "Bearer alice-tok", http.StatusUnauthorized, "", ""},
		{"basic scheme refused", "Basic alice-token", http.StatusUnauthorized, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			present = false
			req := httptest.NewRequest(http.MethodGet, "/v1/loops", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode == http.StatusUnauthorized {
				if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
					t.Fatalf("WWW-Authenticate = %q", got)
				}
				return
			}
			if !present || seen.Kind != tc.wantKind || seen.Name != tc.wantName {
				t.Fatalf("principal = %+v present=%v, want %s/%s", seen, present, tc.wantKind, tc.wantName)
			}
		})
	}
}

func TestSessionStoreLifecycle(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	store := newSessionStore(time.Hour)
	store.now = func() time.Time { return now }

	id, err := store.create(Principal{Kind: "api_token", Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := store.lookup(id)
	if !ok || p.Kind != "session" || p.Name != "alice" {
		t.Fatalf("lookup = %+v %v", p, ok)
	}
	// Use at 50 minutes extends the session; without the extension it
	// would expire at 60.
	now = now.Add(50 * time.Minute)
	if _, ok := store.lookup(id); !ok {
		t.Fatal("session expired early")
	}
	now = now.Add(50 * time.Minute)
	if _, ok := store.lookup(id); !ok {
		t.Fatal("session was not extended on use")
	}
	now = now.Add(2 * time.Hour)
	if _, ok := store.lookup(id); ok {
		t.Fatal("expired session still valid")
	}
	if _, ok := store.lookup("never-issued"); ok {
		t.Fatal("unknown session id accepted")
	}
	id2, _ := store.create(Principal{Name: "bob"})
	store.revoke(id2)
	if _, ok := store.lookup(id2); ok {
		t.Fatal("revoked session still valid")
	}
}

func TestAuthHandlersLoginSessionLogout(t *testing.T) {
	t.Parallel()
	s := gatedServer(t)
	h := s.Handler()

	// Unauthenticated session probe is public and says a credential is needed.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/session", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("session probe = %d", rec.Code)
	}
	var probe struct {
		AuthRequired  bool `json:"auth_required"`
		Authenticated bool `json:"authenticated"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &probe)
	if !probe.AuthRequired || probe.Authenticated {
		t.Fatalf("probe = %+v", probe)
	}

	// Bad token refused, no cookie.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(`{"token":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("bad login = %d cookies=%d", rec.Code, len(rec.Result().Cookies()))
	}

	// Good token mints a cookie with the right attributes; over TLS it is Secure.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(`{"token":"alice-token"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d body=%s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName {
		t.Fatalf("cookies = %+v", cookies)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("login response must be no-store, got %q", rec.Header().Get("Cache-Control"))
	}
	var login struct {
		AuthRequired  bool `json:"auth_required"`
		Authenticated bool `json:"authenticated"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	if !login.AuthRequired || !login.Authenticated {
		t.Fatalf("login payload = %s, want auth_required and authenticated true", rec.Body.String())
	}
	c := cookies[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || !c.Secure || c.Path != "/" || c.MaxAge <= 0 {
		t.Fatalf("cookie attributes = httponly:%v samesite:%v secure:%v path:%q maxage:%d", c.HttpOnly, c.SameSite, c.Secure, c.Path, c.MaxAge)
	}

	// The cookie opens a gated route and the session probe names the holder.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/auth/session", nil)
	req.AddCookie(c)
	h.ServeHTTP(rec, req)
	var who struct {
		Authenticated bool      `json:"authenticated"`
		Principal     Principal `json:"principal"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &who)
	if !who.Authenticated || who.Principal.Kind != "session" || who.Principal.Name != "alice" {
		t.Fatalf("session = %+v", who)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/loops", nil)
	req.AddCookie(c)
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatal("session cookie did not open a gated route")
	}
	if refreshed := rec.Result().Cookies(); len(refreshed) != 1 || refreshed[0].Value != c.Value || refreshed[0].MaxAge <= 0 {
		t.Fatalf("cookie-authenticated request did not refresh the cookie: %+v", refreshed)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "" && cc != "no-store" {
		t.Fatalf("unexpected cache-control on gated route: %q", cc)
	}

	// Plain-HTTP login gets a non-Secure cookie so a LAN console still works.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(`{"token":"alice-token"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if got := rec.Result().Cookies(); len(got) != 1 || got[0].Secure {
		t.Fatalf("plain-http cookie = %+v", got)
	}

	// Logout revokes and clears.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	req.AddCookie(c)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	if got := rec.Result().Cookies(); len(got) != 1 || got[0].MaxAge != -1 {
		t.Fatalf("logout cookie = %+v", got)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/loops", nil)
	req.AddCookie(c)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session still opened a gated route: %d", rec.Code)
	}
}

// serveStatus runs one request through the chain and returns its status.
// The test server has almost no dependencies wired, so a handler that the
// gate lets through may panic on a nil store; that is a handler concern,
// not a gate one, and it reads here as "served" (status 599) rather than
// as a failure, because the gate had already decided.
func serveStatus(h http.Handler, req *http.Request) (status int) {
	rec := httptest.NewRecorder()
	defer func() {
		if r := recover(); r != nil {
			status = 599
		}
	}()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// TestEveryRouteIsGatedOrDeliberatelyPublic is the route-table test: it
// walks every pattern registered on the native mux and proves that a
// request without a credential is refused unless the route is listed as
// public on purpose, and that a credential opens every route. Adding a
// route without deciding its posture fails here.
func TestEveryRouteIsGatedOrDeliberatelyPublic(t *testing.T) {
	t.Parallel()
	s := gatedServer(t)
	h := s.Handler()
	recorded := map[string]bool{}
	for _, p := range s.routes {
		recorded[p] = true
	}
	// Routes mounted by other packages must go through the recorder too;
	// the first three come from the dashboard and the explorer.
	for _, want := range []string{"GET /{$}", "GET /static/{file...}", "GET /docs", "GET /v1/loops", "POST /v1/auth/login"} {
		if !recorded[want] {
			t.Fatalf("route %q was not recorded; a registrar bypassed the route table (recorded %d routes)", want, len(s.routes))
		}
	}
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
			code := serveStatus(h, httptest.NewRequest(method, probePath, strings.NewReader("{}")))
			public := isPublic(method, probePath)
			if public && code == http.StatusUnauthorized {
				t.Fatalf("public route %s refused without a credential", pattern)
			}
			if !public && code != http.StatusUnauthorized {
				t.Fatalf("route %s served without a credential (status %d); gate it or list it in publicRoutes on purpose", pattern, code)
			}
			req := httptest.NewRequest(method, probePath, strings.NewReader("{}"))
			req.Header.Set("Authorization", "Bearer alice-token")
			if code := serveStatus(h, req); code == http.StatusUnauthorized {
				t.Fatalf("route %s refused a valid credential", pattern)
			}
		})
	}
}

func TestPublicRoutesAreExactlyTheIntendedSet(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"GET /health", "GET /v1/version", "GET /v1/identity", "POST /v1/auth/login", "GET /v1/auth/session", "GET /v1/realtime/ws", "POST /v1/companion/observations"} {
		method, path, _ := strings.Cut(p, " ")
		if !isPublic(method, path) {
			t.Errorf("%s should be public", p)
		}
	}
	for _, p := range []string{"GET /v1/loops", "POST /v1/loop-definitions", "GET /v1/system/logs", "POST /v1/chat", "DELETE /v1/contacts/x", "POST /static/x.js", "GET /v1/archive/messages"} {
		method, path, _ := strings.Cut(p, " ")
		if isPublic(method, path) {
			t.Errorf("%s should be gated", p)
		}
	}
	if !isPublic(http.MethodGet, "/static/app.js") || !isPublic(http.MethodGet, "/docs/openapi/native.yaml") {
		t.Error("console assets and the explorer should be public")
	}
}
