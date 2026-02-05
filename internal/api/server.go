// Package api implements the OpenAI-compatible HTTP API.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/router"
)

// Server is the HTTP API server.
type Server struct {
	port   int
	loop   *agent.Loop
	router *router.Router
	logger *slog.Logger
	server *http.Server
}

// NewServer creates a new API server.
func NewServer(port int, loop *agent.Loop, rtr *router.Router, logger *slog.Logger) *Server {
	return &Server{
		port:   port,
		loop:   loop,
		router: rtr,
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

	// Router introspection endpoints
	mux.HandleFunc("GET /v1/router/stats", s.handleRouterStats)
	mux.HandleFunc("GET /v1/router/audit", s.handleRouterAudit)
	mux.HandleFunc("GET /v1/router/explain/{requestId}", s.handleRouterExplain)

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

	agentReq := &agent.Request{
		Messages: req.Messages,
		Model:    req.Model,
	}

	if req.Stream {
		s.handleStreamingCompletion(w, r, agentReq)
		return
	}

	// Non-streaming: run and return complete response
	resp, err := s.loop.Run(r.Context(), agentReq, nil)
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

// StreamChunk is the SSE format for streaming responses.
type StreamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
}

// StreamChoice represents a streaming choice with delta content.
type StreamChoice struct {
	Index        int         `json:"index"`
	Delta        StreamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

// StreamDelta represents incremental content.
type StreamDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

func (s *Server) handleStreamingCompletion(w http.ResponseWriter, r *http.Request, agentReq *agent.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.errorResponse(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	completionID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()
	modelName := "thane" // Will be updated when we get the response

	// Send initial chunk with role
	initialChunk := StreamChunk{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   modelName,
		Choices: []StreamChoice{{
			Index: 0,
			Delta: StreamDelta{Role: "assistant"},
		}},
	}
	s.writeSSE(w, initialChunk)
	flusher.Flush()

	// Stream callback sends each token
	streamCallback := func(token string) {
		chunk := StreamChunk{
			ID:      completionID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   modelName,
			Choices: []StreamChoice{{
				Index: 0,
				Delta: StreamDelta{Content: token},
			}},
		}
		s.writeSSE(w, chunk)
		flusher.Flush()
	}

	// Run agent with streaming
	resp, err := s.loop.Run(r.Context(), agentReq, streamCallback)
	if err != nil {
		s.logger.Error("agent loop failed", "error", err)
		// Can't change status code after streaming started, just close
		return
	}

	// Update model name and send final chunk
	modelName = resp.Model
	finishReason := resp.FinishReason
	finalChunk := StreamChunk{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   modelName,
		Choices: []StreamChoice{{
			Index:        0,
			Delta:        StreamDelta{},
			FinishReason: &finishReason,
		}},
	}
	s.writeSSE(w, finalChunk)
	flusher.Flush()

	// Send [DONE] marker
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (s *Server) writeSSE(w http.ResponseWriter, chunk StreamChunk) {
	data, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", data)
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

// Router introspection handlers

func (s *Server) handleRouterStats(w http.ResponseWriter, r *http.Request) {
	if s.router == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "router not configured")
		return
	}
	
	stats := s.router.GetStats()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func (s *Server) handleRouterAudit(w http.ResponseWriter, r *http.Request) {
	if s.router == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "router not configured")
		return
	}
	
	// Parse limit from query
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	
	decisions := s.router.GetAuditLog(limit)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"count":     len(decisions),
		"decisions": decisions,
	})
}

func (s *Server) handleRouterExplain(w http.ResponseWriter, r *http.Request) {
	if s.router == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "router not configured")
		return
	}
	
	requestID := r.PathValue("requestId")
	if requestID == "" {
		s.errorResponse(w, http.StatusBadRequest, "requestId required")
		return
	}
	
	decision := s.router.Explain(requestID)
	if decision == nil {
		s.errorResponse(w, http.StatusNotFound, "decision not found")
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(decision)
}
