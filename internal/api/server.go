// Package api implements the OpenAI-compatible HTTP API.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
)

// Server is the HTTP API server.
type Server struct {
	port   int
	loop   *agent.Loop
	logger *slog.Logger
	server *http.Server
}

// NewServer creates a new API server.
func NewServer(port int, loop *agent.Loop, logger *slog.Logger) *Server {
	return &Server{
		port:   port,
		loop:   loop,
		logger: logger,
	}
}

// Start begins serving HTTP requests.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// OpenAI-compatible endpoints
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("GET /v1/models", s.handleModels)

	// Health endpoints
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /", s.handleRoot)

	s.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      s.withLogging(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second, // Long for streaming responses
	}

	s.logger.Info("starting API server", "port", s.port)
	return s.server.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(start),
		)
	})
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"name":    "Thane",
		"version": "0.1.0",
		"status":  "ok",
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	// OpenAI-compatible models list
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data": []map[string]any{
			{
				"id":       "thane",
				"object":   "model",
				"created":  time.Now().Unix(),
				"owned_by": "thane",
			},
		},
	})
}

// ChatCompletionRequest is the OpenAI-compatible request format.
type ChatCompletionRequest struct {
	Model    string          `json:"model"`
	Messages []agent.Message `json:"messages"`
	Stream   bool            `json:"stream,omitempty"`
}

// ChatCompletionResponse is the OpenAI-compatible response format.
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice represents a completion choice.
type Choice struct {
	Index        int           `json:"index"`
	Message      agent.Message `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

// Usage represents token usage.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// TODO: Handle streaming (req.Stream)
	if req.Stream {
		s.errorResponse(w, http.StatusNotImplemented, "streaming not yet implemented")
		return
	}

	// Run through the agent loop
	agentReq := &agent.Request{
		Messages: req.Messages,
		Model:    req.Model,
	}

	resp, err := s.loop.Run(r.Context(), agentReq)
	if err != nil {
		s.logger.Error("agent loop failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "agent error")
		return
	}

	// Format as OpenAI response
	completion := ChatCompletionResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Choices: []Choice{
			{
				Index: 0,
				Message: agent.Message{
					Role:    "assistant",
					Content: resp.Content,
				},
				FinishReason: resp.FinishReason,
			},
		},
		Usage: Usage{
			PromptTokens:     0, // TODO: actual counting
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(completion)
}

func (s *Server) errorResponse(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    code,
		},
	})
}
