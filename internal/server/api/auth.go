package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/server/edge"
	"github.com/nugget/thane-ai-agent/internal/server/legacyroute"
	"github.com/nugget/thane-ai-agent/internal/server/listen"
)

// Principal is the identity a request on the native API established.
// Kind says how: "api_token" for an operator credential from
// listen.auth.tokens, "companion" for a companion account token, "session"
// for a console cookie minted from one of those, and "device_cert" for a
// client certificate the HTTPS front door verified. Name is the token
// label, the account, or the certificate subject; never the secret.
type Principal struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type principalKey struct{}

// PrincipalFromContext returns the authenticated principal for a request,
// if the gate established one.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// sessionCookieName is the console's session cookie. Sessions are the
// console's credential shape; API clients use bearer tokens and never
// see a cookie.
const sessionCookieName = "thane_session"

// authGate is the native API's authentication middleware. It is nil when
// no operator token is configured, and a nil gate passes everything, so
// the open-by-default behaviour of an unconfigured install is preserved
// and thane init is what closes it.
type authGate struct {
	api        *listen.TokenSet
	companions *listen.TokenSet
	sessions   *sessionStore
	logger     *slog.Logger
}

func newAuthGate(cfg config.ListenAuthConfig, companionTokens map[string]string, logger *slog.Logger) *authGate {
	if !cfg.Configured() {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &authGate{
		api:        listen.NewTokenSet(cfg.TokenIndex()),
		companions: listen.NewTokenSet(companionTokens),
		sessions:   newSessionStore(cfg.SessionTTL),
		logger:     logger.With("subsystem", "auth"),
	}
}

// publicRoutes are the native routes that serve without a credential.
// Each entry is a reason as much as a rule: the boot gate reads /health,
// supervisors read /v1/version, /v1/identity is evidence published for
// clients to pin, the console shell and assets must load to show the
// sign-in, the auth endpoints authenticate, and the companion endpoints
// carry their own credential in-band. Everything not listed here is
// gated, and the route-table test holds that line: a new route is gated
// unless it is added here on purpose.
var publicRoutes = map[string]bool{
	"GET /health":                     true,
	"GET /v1/version":                 true,
	"GET /v1/identity":                true,
	"GET /":                           true,
	"GET /{$}":                        true,
	"POST /v1/auth/login":             true,
	"POST /v1/auth/logout":            true,
	"GET /v1/auth/session":            true,
	"GET /v1/realtime/ws":             true,
	"POST /v1/companion/observations": true,
}

// publicPrefixes are path prefixes that serve without a credential: the
// console's static assets and the OpenAPI explorer, which documents a
// contract that is already public in the repository.
var publicPrefixes = []string{"/static/", "/docs"}

// principalCompanion is the Kind authenticate assigns to a companion
// account token. It is the one principal kind the gate restricts by
// route: a companion credential lives in a phone's Keychain and on a
// laptop, and it authenticates a device offering data, not an operator
// driving the API.
const principalCompanion = "companion"

// companionSurfaceRoutes is the companion's whole reachable surface —
// the same literals server.go binds to the companion handlers. The
// legacy aliases join in init from legacyroute.Aliases rather than as a
// second hand-copied list (#1084).
//
// Every entry is also in publicRoutes today, so a companion token grants
// no gated access at all. They are enumerated anyway so that gating one
// later — moving the realtime handshake onto the Authorization header,
// say — cannot silently lock a companion out of its own surface.
// companion_scope_test.go proves this set and the routes server.go
// actually registers agree, derived from the route table rather than
// from parsing source.
var companionSurfaceRoutes = []string{
	"GET /v1/realtime/ws",
	"POST /v1/companion/observations",
}

// companionRoutes is companionSurfaceRoutes plus the legacy aliases,
// indexed for lookup.
var companionRoutes = map[string]bool{}

func init() {
	for _, alias := range legacyroute.Aliases {
		publicRoutes[alias.Route()] = true
	}
	for _, route := range companionSurfaceRoutes {
		companionRoutes[route] = true
	}
	// The aliases are companion surface for the same reason they are
	// public: they are the companion WebSocket under an older name.
	for _, alias := range legacyroute.Aliases {
		companionRoutes[alias.Route()] = true
	}
}

// companionMayReach reports whether a companion credential is permitted
// on a route. Deny by default: a route added to the server is closed to
// companions until it is named here on purpose.
func companionMayReach(method, path string) bool {
	return companionRoutes[method+" "+path]
}

// isPublic reports whether the request's route is one that serves without
// a credential.
func isPublic(method, path string) bool {
	if publicRoutes[method+" "+path] {
		return true
	}
	if path == "/" {
		return method == http.MethodGet
	}
	for _, prefix := range publicPrefixes {
		if strings.HasPrefix(path, prefix) {
			return method == http.MethodGet || method == http.MethodHead
		}
	}
	return false
}

// wrap enforces the gate ahead of the route table. A nil gate is open.
func (g *authGate) wrap(next http.Handler) http.Handler {
	if g == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublic(r.Method, r.URL.Path) {
			// A credential on a public route still identifies the caller
			// to the handler (the session endpoint relies on it), it just
			// is not required.
			if p, ok := g.authenticate(r); ok {
				r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
			}
			next.ServeHTTP(w, r)
			return
		}
		p, ok := g.authenticate(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="thane"`)
			writeUnauthorized(w)
			return
		}
		if p.Kind == principalCompanion && !companionMayReach(r.Method, r.URL.Path) {
			// The credential is valid; it is simply not an operator's.
			// 403 rather than 401 so a companion does not retry, and so
			// the refusal is distinguishable from a bad token.
			writeForbidden(w, "a companion credential may not reach this route")
			return
		}
		if p.Kind == "session" {
			g.refreshCookie(w, r)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

// refreshCookie re-issues the session cookie with a full lifetime, so the
// browser's copy slides with the server-side session instead of expiring
// a fixed TTL after sign-in while the session itself is still live.
func (g *authGate) refreshCookie(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return
	}
	http.SetCookie(w, sessionCookie(c.Value, g.sessions.ttl, requestIsTLS(r)))
}

// sessionCookie is the one shape the console's cookie ever takes.
func sessionCookie(id string, ttl time.Duration, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
		MaxAge:   int(ttl / time.Second),
	}
}

// authenticate resolves the request's credential in order: a client
// certificate the front door verified, a bearer token (operator tokens,
// then companion account tokens), then the console session cookie.
func (g *authGate) authenticate(r *http.Request) (Principal, bool) {
	if dp, ok := edge.PrincipalFromContext(r.Context()); ok {
		return Principal{Kind: "device_cert", Name: dp.Subject}, true
	}
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		if label, ok := g.api.Match(token); ok {
			return Principal{Kind: "api_token", Name: label}, true
		}
		if account, ok := g.companions.Match(token); ok {
			return Principal{Kind: principalCompanion, Name: account}, true
		}
		return Principal{}, false
	}
	if c, err := r.Cookie(sessionCookieName); err == nil {
		if p, ok := g.sessions.lookup(c.Value); ok {
			return p, true
		}
	}
	return Principal{}, false
}

// authRequired reports whether the gate is enforcing, for the session
// endpoint's answer to an unauthenticated console.
func (g *authGate) authRequired() bool { return g != nil }

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":{"message":"authentication required","type":"unauthorized"}}`))
}

// writeForbidden refuses a caller whose credential is valid but whose
// principal is not permitted here.
func writeForbidden(w http.ResponseWriter, message string) {
	// Marshal before writing anything, and carry no logger: writeJSON
	// dereferences its logger when encoding fails (server.go:51-54), and
	// the gate has no logger to hand it on every path. A client that
	// disconnects mid-response must not panic the middleware.
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": "forbidden"},
	})
	if err != nil {
		body = []byte(`{"error":{"message":"forbidden","type":"forbidden"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write(body)
}

// --- sessions ---------------------------------------------------------

type session struct {
	principal Principal
	expires   time.Time
}

// errCompanionSession refuses a console session for a companion
// credential.
var errCompanionSession = errors.New("a companion credential cannot open a console session")

// sessionStore holds console sessions in memory. Losing them on restart
// costs one sign-in and buys a store with no persistence, no secret key,
// and nothing to rotate.
type sessionStore struct {
	mu       sync.Mutex
	ttl      time.Duration
	sessions map[string]session
	now      func() time.Time
}

func newSessionStore(ttl time.Duration) *sessionStore {
	if ttl <= 0 {
		ttl = 168 * time.Hour
	}
	return &sessionStore{ttl: ttl, sessions: map[string]session{}, now: time.Now}
}

// create mints a session for the principal and returns its id.
//
// A companion principal is refused at the store, not only at the one
// handler that mints sessions today. A console session is an operator's
// browser; a companion credential must never become one, whatever
// future path reaches this store.
func (s *sessionStore) create(p Principal) (string, error) {
	if p.Kind == principalCompanion {
		return "", errCompanionSession
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.sessions[id] = session{principal: p, expires: s.now().Add(s.ttl)}
	return id, nil
}

// lookup resolves a session id, extending its life on use. The returned
// principal reports kind "session" and carries the name of the
// credential that opened it.
func (s *sessionStore) lookup(id string) (Principal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return Principal{}, false
	}
	now := s.now()
	if !now.Before(sess.expires) {
		delete(s.sessions, id)
		return Principal{}, false
	}
	sess.expires = now.Add(s.ttl)
	s.sessions[id] = sess
	return Principal{Kind: "session", Name: sess.principal.Name}, true
}

func (s *sessionStore) revoke(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// sweepLocked drops expired sessions; called with the lock held on the
// write path so the map cannot grow without bound from abandoned logins.
func (s *sessionStore) sweepLocked() {
	now := s.now()
	for id, sess := range s.sessions {
		if !now.Before(sess.expires) {
			delete(s.sessions, id)
		}
	}
}

// --- handlers ---------------------------------------------------------

// handleAuthLogin exchanges a token for a session cookie. The console is
// the intended caller; it posts the token once and never stores it. The
// cookie is HttpOnly and SameSite=Strict, so script cannot read it and a
// cross-site page cannot send it, and it is marked Secure whenever the
// request arrived over TLS, directly or through a proxy that says so.
func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		s.errorResponse(w, http.StatusNotFound, "authentication is not configured")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Token) == "" {
		s.errorResponse(w, http.StatusBadRequest, "token is required")
		return
	}
	var p Principal
	if label, ok := s.auth.api.Match(req.Token); ok {
		p = Principal{Kind: "api_token", Name: label}
	} else if account, ok := s.auth.companions.Match(req.Token); ok {
		// A companion token authenticates a device offering data, not an
		// operator. Refuse before minting rather than relying on the
		// store's backstop, so the caller gets a reason.
		s.auth.logger.Warn("console sign-in refused for companion credential",
			"account", account, "remote", r.RemoteAddr)
		s.errorResponse(w, http.StatusForbidden, errCompanionSession.Error())
		return
	} else {
		s.auth.logger.Warn("console sign-in refused", "remote", r.RemoteAddr)
		writeUnauthorized(w)
		return
	}
	id, err := s.auth.sessions.create(p)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "could not create session")
		return
	}
	http.SetCookie(w, sessionCookie(id, s.auth.sessions.ttl, requestIsTLS(r)))
	s.auth.logger.Info("console sign-in", "principal", p.Name, "kind", p.Kind, "remote", r.RemoteAddr)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{"auth_required": true, "authenticated": true, "principal": p}, s.logger)
}

// handleAuthLogout revokes the session cookie, if any, and clears it.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if s.auth != nil {
		if c, err := r.Cookie(sessionCookieName); err == nil {
			s.auth.sessions.revoke(c.Value)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: requestIsTLS(r), MaxAge: -1,
	})
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// handleAuthSession tells the caller whether a credential is required and
// whether it has one. It is public so the console can decide to show the
// sign-in without first collecting a 401.
func (s *Server) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"auth_required": s.auth.authRequired(), "authenticated": false}
	if p, ok := PrincipalFromContext(r.Context()); ok {
		resp["authenticated"] = true
		resp["principal"] = p
	}
	// The answer varies by credential and names the holder: never let a
	// shared cache hand one caller's session state to another.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, resp, s.logger)
}

// requestIsTLS reports whether the request arrived over TLS, either on
// this listener or at a proxy that recorded the scheme.
func requestIsTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// routeTable records every pattern registered on the native mux so the
// route-table test can prove each one is either gated or deliberately
// public. It keeps the mux's method names, so the OpenAPI coverage test,
// which reads registration literals from this package's source, sees no
// change.
type routeTable struct {
	*http.ServeMux
	patterns []string
}

func newRouteTable() *routeTable { return &routeTable{ServeMux: http.NewServeMux()} }

func (m *routeTable) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	m.patterns = append(m.patterns, pattern)
	m.ServeMux.HandleFunc(pattern, handler)
}

func (m *routeTable) Handle(pattern string, handler http.Handler) {
	m.patterns = append(m.patterns, pattern)
	m.ServeMux.Handle(pattern, handler)
}
