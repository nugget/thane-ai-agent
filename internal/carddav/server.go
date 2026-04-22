package carddav

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	libcarddav "github.com/emersion/go-webdav/carddav"
	"github.com/nugget/thane-ai-agent/internal/logging"
)

// Server wraps a CardDAV handler with HTTP server lifecycle, Basic
// Auth middleware, and multi-listen support with dynamic rebinding.
//
// Multiple listen addresses can be configured (e.g., localhost and a
// Tailscale interface).  If some fail to bind at startup, the server
// logs warnings and continues.  A background rebind loop periodically
// retries failed addresses so that interfaces that appear after
// startup (like VPN/Tailscale) are picked up automatically.
type Server struct {
	addrs    []string
	username string
	password string
	backend  *Backend
	logger   *slog.Logger
	handler  http.Handler

	mu      sync.Mutex
	servers map[string]*http.Server
	unbound map[string]struct{}
	cancel  context.CancelFunc
}

// NewServer creates a CardDAV server.  The server is created but not
// started.  Call [Server.Start] to begin serving requests.
func NewServer(addrs []string, username, password string, backend *Backend, logger *slog.Logger) *Server {
	return &Server{
		addrs:    addrs,
		username: username,
		password: password,
		backend:  backend,
		logger:   logger,
		servers:  make(map[string]*http.Server),
		unbound:  make(map[string]struct{}),
	}
}

// Start begins serving CardDAV requests on all configured addresses.
// It returns an error only if zero listeners bind successfully.  For
// partial failures, warnings are logged and the rebind loop will retry.
func (s *Server) Start(ctx context.Context) error {
	s.handler = s.buildHandler()

	ctx, s.cancel = context.WithCancel(ctx)

	bound := 0
	for _, addr := range s.addrs {
		if err := s.bind(addr); err != nil {
			s.logger.Warn("carddav: failed to bind",
				"address", addr, "error", err)
			s.mu.Lock()
			s.unbound[addr] = struct{}{}
			s.mu.Unlock()
		} else {
			bound++
		}
	}

	if bound == 0 {
		s.cancel()
		return fmt.Errorf("carddav: failed to bind any address")
	}

	go s.rebindLoop(ctx)
	return nil
}

// Shutdown gracefully stops all running listeners and the rebind
// loop.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var firstErr error
	for addr, srv := range s.servers {
		if err := srv.Shutdown(ctx); err != nil {
			s.logger.Error("carddav: shutdown failed",
				"address", addr, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// bind creates a listener and starts serving on the given address.
func (s *Server) bind(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Handler:      s.handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	s.mu.Lock()
	s.servers[addr] = srv
	s.mu.Unlock()

	s.logger.Info("carddav: listening", "address", addr)

	go s.serve(addr, srv, ln)
	return nil
}

// serve runs the HTTP server.  On non-shutdown errors (e.g., interface
// dropped), the address is moved to the unbound set for the rebind
// loop to retry.
func (s *Server) serve(addr string, srv *http.Server, ln net.Listener) {
	err := srv.Serve(ln)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.logger.Warn("carddav: listener stopped",
			"address", addr, "error", err)
		s.mu.Lock()
		delete(s.servers, addr)
		s.unbound[addr] = struct{}{}
		s.mu.Unlock()
	}
}

// rebindLoop periodically retries binding addresses that failed.
func (s *Server) rebindLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tryRebind()
		}
	}
}

// tryRebind attempts to bind each address in the unbound set.
func (s *Server) tryRebind() {
	s.mu.Lock()
	addrs := make([]string, 0, len(s.unbound))
	for addr := range s.unbound {
		addrs = append(addrs, addr)
	}
	s.mu.Unlock()

	for _, addr := range addrs {
		if err := s.bind(addr); err != nil {
			s.logger.Debug("carddav: rebind failed",
				"address", addr, "error", err)
		} else {
			s.mu.Lock()
			delete(s.unbound, addr)
			s.mu.Unlock()
			s.logger.Info("carddav: rebound successfully",
				"address", addr)
		}
	}
}

// buildHandler creates the HTTP handler chain: auth → logging →
// carddav.Handler.
func (s *Server) buildHandler() http.Handler {
	davHandler := &libcarddav.Handler{
		Backend: s.backend,
		Prefix:  "/carddav",
	}
	return s.withAuth(s.withLogging(davHandler))
}

// withAuth wraps a handler with HTTP Basic Auth.  The .well-known
// redirect is served without authentication so clients can discover
// the CardDAV endpoint.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve .well-known discovery without authentication so
		// clients can find the CardDAV endpoint.  Accept with or
		// without trailing slash.
		clean := strings.TrimRight(r.URL.Path, "/")
		if clean == "/.well-known/carddav" {
			http.Redirect(w, r, "/carddav/", http.StatusMovedPermanently)
			return
		}

		user, pass, ok := r.BasicAuth()
		// Use constant-time comparison for both username and
		// password to avoid leaking credential info via timing.
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.username)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(s.password)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="Thane CardDAV"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withLogging wraps a handler with request logging.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := logging.NewAccessResponseWriter(w)
		next.ServeHTTP(rw, r)
		s.logger.Info("request handled",
			"kind", logging.KindHTTPAccess,
			"server", "carddav",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.StatusCode(),
			"response_bytes", rw.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}
