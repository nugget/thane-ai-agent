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
	"github.com/nugget/thane-ai-agent/internal/server/listen"
)

// OpenAIServer is a dedicated server for the OpenAI-compatible API surface.
// It runs on its own port (default 8081) so the frozen OpenAI shim is cleanly
// separated from the Thane-native /v1 API on the primary listen port —
// mirroring the OllamaServer split. The handlers live on *Server; this type
// owns only the listener and route registration.
type OpenAIServer struct {
	address        string
	apiKey         string // bearer token required when non-empty; see openaiAuth
	allowedSources []netip.Prefix
	port           int

	api         *Server
	logger      *slog.Logger
	server      *http.Server
	handler     http.Handler
	handlerOnce sync.Once
}

// NewOpenAIServer creates an OpenAI-compatible API server backed by the native
// Server's handlers, listening on its own port.
//
// Parameters:
//   - address: IP address to bind to (empty string binds to all interfaces)
//   - port: Port to listen on (default 8081)
//   - apiKey: bearer token every request must present; empty leaves the
//     surface open (see config.OpenAIAPIConfig.APIKey)
//   - allowedSources: When non-empty, only callers inside one of these
//     prefixes are served; empty leaves the surface open to the network
//   - apiServer: The native Server whose OpenAI-compatible handlers are served
//   - logger: Logger for request and error logging
//
// The server is created but not started. Call Start to begin serving requests.
func NewOpenAIServer(address string, port int, apiKey string, allowedSources []netip.Prefix, apiServer *Server, logger *slog.Logger) *OpenAIServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &OpenAIServer{
		address:        address,
		port:           port,
		apiKey:         apiKey,
		allowedSources: allowedSources,
		api:            apiServer,
		logger:         logger,
	}
}

// Start begins serving the OpenAI-compatible surface and blocks until the
// server is shut down or encounters an error. Endpoints:
//   - POST /v1/chat/completions - OpenAI chat completions (streaming supported)
//   - GET /v1/models - OpenAI-shaped model list
//   - GET /v1/version, GET /health - health checks for the standalone server
//
// Use Shutdown to gracefully stop the server.
func (s *OpenAIServer) Start(ctx context.Context) error {
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
	s.logger.Info("starting OpenAI-compatible API server", "address", addr, "port", s.port)
	// ErrServerClosed is the expected return on graceful Shutdown; don't surface
	// it as an error (App.Serve would log it). Mirrors the other API servers.
	if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler returns the OpenAI surface's complete handler chain, built once
// and shared by Start and the HTTPS front door.
func (s *OpenAIServer) Handler() http.Handler {
	s.handlerOnce.Do(func() {
		mux := http.NewServeMux()
		s.api.RegisterOpenAIRoutes(mux)
		// The source allowlist runs ahead of the bearer gate, so a key is
		// only ever compared for a caller the operator admitted.
		s.handler = s.withLogging(listen.SecurityHeaders(listen.PostureAPI,
			listen.RestrictSources(s.logger, "openai", s.allowedSources,
				listen.RejectCrossOriginWrites(s.logger, openaiAuth(s.apiKey, listen.LimitRequestBody(compatMaxBodyBytes, mux))))))
	})
	return s.handler
}

// Shutdown gracefully stops the server. Returns nil if it was never started.
func (s *OpenAIServer) Shutdown(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

// withLogging wraps an HTTP handler to log request details.
func (s *OpenAIServer) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := logging.NewAccessResponseWriter(w)
		next.ServeHTTP(rw, r)
		s.logger.Info("request handled",
			"kind", logging.KindHTTPAccess,
			"server", "openai",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.StatusCode(),
			"response_bytes", rw.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

// RegisterOpenAIRoutes registers the OpenAI-compatible surface on mux: the
// chat-completions and model-list endpoints, plus health/version so the
// server answers standalone health checks. This surface is served on its own
// port by OpenAIServer, separate from the Thane-native /v1 API.
func (s *Server) RegisterOpenAIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("GET /v1/version", s.handleVersion)
	mux.HandleFunc("GET /health", s.handleHealth)
}
