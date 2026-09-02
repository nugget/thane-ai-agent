package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/server/listen"
)

// OllamaServer is a dedicated server for Ollama-compatible API endpoints.
// It runs on a separate port (default 11434) to avoid conflicts with the native API.
//
// Use this for dual-port setups where you want Ollama compatibility on a dedicated
// port (e.g., for Home Assistant integration). For single-port setups, use
// Server.RegisterOllamaRoutes instead.
type OllamaServer struct {
	address        string
	port           int
	apiKey         string
	allowedSources []netip.Prefix

	loop        *agent.Loop
	owuTracker  *OWUTracker
	logger      *slog.Logger
	server      *http.Server
	handler     http.Handler
	handlerOnce sync.Once
}

// SetOWUTracker configures the Open WebUI loop tracker for dashboard visibility.
func (s *OllamaServer) SetOWUTracker(t *OWUTracker) {
	s.owuTracker = t
}

// NewOllamaServer creates a new Ollama-compatible API server.
//
// Parameters:
//   - address: IP address to bind to (empty string binds to all interfaces)
//   - port: Port to listen on (typically 11434 for Ollama compatibility)
//   - apiKey: When non-empty, every request must present this value as a
//     bearer token; empty leaves the surface unauthenticated
//   - allowedSources: When non-empty, only callers inside one of these
//     prefixes are served; empty leaves the surface open to the network
//   - loop: The agent loop that processes requests
//   - logger: Logger for request and error logging
//
// The server is created but not started. Call Start to begin serving requests.
func NewOllamaServer(address string, port int, apiKey string, allowedSources []netip.Prefix, loop *agent.Loop, logger *slog.Logger) *OllamaServer {
	return &OllamaServer{
		address:        address,
		port:           port,
		apiKey:         apiKey,
		allowedSources: allowedSources,
		loop:           loop,
		logger:         logger,
	}
}

// Start begins serving Ollama-compatible HTTP requests.
// This method blocks until the server is shut down or encounters an error.
//
// The server listens on the address and port specified during creation.
// It implements the following Ollama API endpoints:
//   - POST /api/chat - Main conversation endpoint
//   - POST /api/generate - Not implemented; returns 501 (use /api/chat)
//   - GET /api/tags - List available models
//   - GET /api/version - Get server version
//   - GET / and HEAD / - Health check endpoints
//
// Use Shutdown to gracefully stop the server.
func (s *OllamaServer) Start(ctx context.Context) error {
	s.server = listen.NewServer(
		fmt.Sprintf("%s:%d", s.address, s.port),
		s.Handler(),
		30*time.Second,
		300*time.Second, // Long for slow models
	)

	addr := s.address
	if addr == "" {
		addr = "0.0.0.0"
	}
	s.logger.Info("starting Ollama-compatible API server", "address", addr, "port", s.port)
	// ErrServerClosed is the expected return on graceful Shutdown; don't surface
	// it as an error (App.Serve would log it). Mirrors the other API servers.
	if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler returns the Ollama surface's complete handler chain, built once
// and shared by Start and the HTTPS front door.
func (s *OllamaServer) Handler() http.Handler {
	s.handlerOnce.Do(func() {
		s.handler = s.buildHandler()
	})
	return s.handler
}

// buildHandler assembles the route table and middleware chain.
func (s *OllamaServer) buildHandler() http.Handler {
	mux := http.NewServeMux()

	// Ollama-compatible endpoints
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("POST /api/generate", s.handleGenerate)
	mux.HandleFunc("GET /api/tags", s.handleTags)
	mux.HandleFunc("GET /api/version", s.handleVersion)

	// Health check - matches root path only (not a prefix match)
	mux.HandleFunc("HEAD /{$}", s.handleHead)
	mux.HandleFunc("GET /{$}", s.handleHealth)

	// The guards sit inside logging so rejected requests still produce
	// access-log lines with their 401/403 status. The source allowlist
	// goes first: a caller the operator has not admitted costs one log
	// line and nothing else.
	return s.withLogging(listen.RestrictSources(s.logger, "ollama", s.allowedSources,
		listen.RejectCrossOriginWrites(s.logger, ollamaAuth(s.apiKey, mux))))
}

// Shutdown gracefully stops the server.
//
// This method should be called to cleanly shut down the server, allowing it
// to finish processing active requests. The provided context can be used to
// set a deadline for the shutdown process.
//
// If the server was never started or has already been shut down, this method
// returns nil.
func (s *OllamaServer) Shutdown(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

// withLogging wraps an HTTP handler to log request details.
// Each request is logged with method, path, and duration.
func (s *OllamaServer) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := logging.NewAccessResponseWriter(w)
		next.ServeHTTP(rw, r)
		s.logger.Info("request handled",
			"kind", logging.KindHTTPAccess,
			"server", "ollama",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.StatusCode(),
			"response_bytes", rw.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

// handleHead responds to HEAD / for health checks.
// Returns 200 OK with no body, as expected by HTTP HEAD semantics.
func (s *OllamaServer) handleHead(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handleHealth responds to GET / for health checks.
// Returns a simple JSON status object indicating the server is operational.
func (s *OllamaServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
		s.logger.Debug("health check write failed", "error", err)
	}
}

// handleChat handles POST /api/chat (main conversation endpoint).
// This is the primary Ollama API endpoint for multi-turn conversations.
// Request format and behavior matches Ollama's chat API.
func (s *OllamaServer) handleChat(w http.ResponseWriter, r *http.Request) {
	// Delegate to shared implementation
	handleOllamaChatShared(w, r, s.loop, s.owuTracker, s.logger)
}

// handleGenerate handles POST /api/generate.
//
// Ollama's generate endpoint takes a prompt-based body ({model, prompt, ...})
// and returns a generation-shaped response (a top-level "response" string, not
// a "message" object). Thane does not implement that path: the only foreign
// clients on this frozen compat surface — Home Assistant's Ollama integration
// and open-webui — drive Thane exclusively through POST /api/chat, so generate
// has no consumer.
//
// Decoding a prompt body as a chat request yields an empty messages slice and a
// blank turn, which reads as "the model said nothing" rather than "this endpoint
// is unsupported". Reject it honestly with 501 and point callers at /api/chat
// instead. The hit is logged at Warn so that a real generate consumer, should
// one ever appear, surfaces in the logs and can justify implementing the
// prompt-based path.
func (s *OllamaServer) handleGenerate(w http.ResponseWriter, r *http.Request) {
	s.logger.Warn("ollama /api/generate is not implemented; rejecting request",
		"remote_addr", r.RemoteAddr,
		"x_forwarded_for", r.Header.Get("X-Forwarded-For"),
		"user_agent", r.Header.Get("User-Agent"),
	)
	ollamaError(w, http.StatusNotImplemented,
		"/api/generate is not implemented; use POST /api/chat with a messages array")
}

// handleTags handles GET /api/tags (list models).
// Returns the exposed virtual execution policies in Ollama's expected format.
func (s *OllamaServer) handleTags(w http.ResponseWriter, r *http.Request) {
	handleOllamaTagsShared(w, r, s.logger)
}

// handleVersion handles GET /api/version.
// Returns version information in Ollama's expected format.
func (s *OllamaServer) handleVersion(w http.ResponseWriter, r *http.Request) {
	handleOllamaVersionShared(w, r, s.logger)
}
