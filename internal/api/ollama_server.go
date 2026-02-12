// Package api implements the Ollama-compatible HTTP API as a separate server.
// This allows Thane to expose an Ollama-compatible API on port 11434 for
// Home Assistant integration, while keeping the native API on port 8080.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
)

// OllamaServer is a dedicated server for Ollama-compatible API endpoints.
// It runs on a separate port (default 11434) to avoid conflicts with the native API.
//
// Use this for dual-port setups where you want Ollama compatibility on a dedicated
// port (e.g., for Home Assistant integration). For single-port setups, use
// Server.RegisterOllamaRoutes instead.
type OllamaServer struct {
	address string
	port    int
	loop    *agent.Loop
	logger  *slog.Logger
	server  *http.Server
}

// NewOllamaServer creates a new Ollama-compatible API server.
//
// Parameters:
//   - address: IP address to bind to (empty string binds to all interfaces)
//   - port: Port to listen on (typically 11434 for Ollama compatibility)
//   - loop: The agent loop that processes requests
//   - logger: Logger for request and error logging
//
// The server is created but not started. Call Start to begin serving requests.
func NewOllamaServer(address string, port int, loop *agent.Loop, logger *slog.Logger) *OllamaServer {
	return &OllamaServer{
		address: address,
		port:    port,
		loop:    loop,
		logger:  logger,
	}
}

// Start begins serving Ollama-compatible HTTP requests.
// This method blocks until the server is shut down or encounters an error.
//
// The server listens on the address and port specified during creation.
// It implements the following Ollama API endpoints:
//   - POST /api/chat - Main conversation endpoint
//   - POST /api/generate - Simple completion endpoint
//   - GET /api/tags - List available models
//   - GET /api/version - Get server version
//   - GET / and HEAD / - Health check endpoints
//
// Use Shutdown to gracefully stop the server.
func (s *OllamaServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Ollama-compatible endpoints
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("POST /api/generate", s.handleGenerate)
	mux.HandleFunc("GET /api/tags", s.handleTags)
	mux.HandleFunc("GET /api/version", s.handleVersion)

	// Health check - matches root path only (not a prefix match)
	mux.HandleFunc("HEAD /{$}", s.handleHead)
	mux.HandleFunc("GET /{$}", s.handleHealth)

	s.server = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", s.address, s.port),
		Handler:      s.withLogging(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 300 * time.Second, // Long for slow models
	}

	addr := s.address
	if addr == "" {
		addr = "0.0.0.0"
	}
	s.logger.Info("starting Ollama-compatible API server", "address", addr, "port", s.port)
	return s.server.ListenAndServe()
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
		next.ServeHTTP(w, r)
		s.logger.Info("ollama request",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(start),
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
	handleOllamaChatShared(w, r, s.loop, s.logger)
}

// handleGenerate handles POST /api/generate (simple completion).
// This endpoint provides single-turn text completion for compatibility.
// Currently implemented as a single-turn chat for simplicity.
func (s *OllamaServer) handleGenerate(w http.ResponseWriter, r *http.Request) {
	// For now, treat generate like a single-turn chat
	handleOllamaChatShared(w, r, s.loop, s.logger)
}

// handleTags handles GET /api/tags (list models).
// Returns a list of available models in Ollama's expected format.
// Currently returns "thane:latest" as the only available model.
func (s *OllamaServer) handleTags(w http.ResponseWriter, r *http.Request) {
	handleOllamaTagsShared(w, r, s.logger)
}

// handleVersion handles GET /api/version.
// Returns version information in Ollama's expected format.
func (s *OllamaServer) handleVersion(w http.ResponseWriter, r *http.Request) {
	handleOllamaVersionShared(w, r, s.logger)
}
