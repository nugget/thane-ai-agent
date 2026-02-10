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
type OllamaServer struct {
	address string
	port    int
	loop    *agent.Loop
	logger  *slog.Logger
	server  *http.Server
}

// NewOllamaServer creates a new Ollama-compatible API server.
func NewOllamaServer(address string, port int, loop *agent.Loop, logger *slog.Logger) *OllamaServer {
	return &OllamaServer{
		address: address,
		port:    port,
		loop:   loop,
		logger: logger,
	}
}

// Start begins serving Ollama-compatible HTTP requests.
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
func (s *OllamaServer) Shutdown(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

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
func (s *OllamaServer) handleHead(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handleHealth responds to GET / for health checks.
func (s *OllamaServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
		s.logger.Debug("health check write failed", "error", err)
	}
}

// handleChat handles POST /api/chat (main conversation endpoint).
func (s *OllamaServer) handleChat(w http.ResponseWriter, r *http.Request) {
	// Delegate to shared implementation
	handleOllamaChatShared(w, r, s.loop, s.logger)
}

// handleGenerate handles POST /api/generate (simple completion).
func (s *OllamaServer) handleGenerate(w http.ResponseWriter, r *http.Request) {
	// For now, treat generate like a single-turn chat
	handleOllamaChatShared(w, r, s.loop, s.logger)
}

// handleTags handles GET /api/tags (list models).
func (s *OllamaServer) handleTags(w http.ResponseWriter, r *http.Request) {
	handleOllamaTagsShared(w, r, s.logger)
}

// handleVersion handles GET /api/version.
func (s *OllamaServer) handleVersion(w http.ResponseWriter, r *http.Request) {
	handleOllamaVersionShared(w, r, s.logger)
}
