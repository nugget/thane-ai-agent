// Package api implements the OpenAI-compatible HTTP API.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/buildinfo"
	"github.com/nugget/thane-ai-agent/internal/checkpoint"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/web"
)

// writeJSON encodes v as JSON to w, logging any errors at debug level.
// Errors here typically mean the client disconnected mid-response,
// which is not actionable but worth tracking for debugging.
func writeJSON(w http.ResponseWriter, v any, logger *slog.Logger) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Debug("failed to write JSON response", "error", err)
	}
}

// Server is the HTTP API server.
type Server struct {
	address      string
	port         int
	loop         *agent.Loop
	router       *router.Router
	checkpointer *checkpoint.Checkpointer
	memoryStore  *memory.SQLiteStore
	archiveStore *memory.ArchiveStore
	logger       *slog.Logger
	server       *http.Server
	stats        *SessionStats
}

// SessionStats tracks token usage and cost for the current session.
type SessionStats struct {
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	TotalRequests     int64   `json:"total_requests"`
	EstimatedCostUSD  float64 `json:"estimated_cost_usd"`
	ReportedBalance   float64 `json:"reported_balance_usd,omitempty"`
	BalanceSetAt      string  `json:"balance_set_at,omitempty"`
	mu                sync.Mutex
}

// Model pricing per million tokens (USD)
var modelPricing = map[string][2]float64{
	// [input_per_1M, output_per_1M]
	"claude-opus-4-20250514":   {15.0, 75.0},
	"claude-sonnet-4-20250514": {3.0, 15.0},
	"claude-haiku-3-20240307":  {0.25, 1.25},
}

func (s *SessionStats) Record(model string, inputTokens, outputTokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalInputTokens += int64(inputTokens)
	s.TotalOutputTokens += int64(outputTokens)
	s.TotalRequests++

	pricing, ok := modelPricing[model]
	if !ok {
		pricing = [2]float64{15.0, 75.0} // default to Opus pricing
	}
	s.EstimatedCostUSD += float64(inputTokens) / 1_000_000.0 * pricing[0]
	s.EstimatedCostUSD += float64(outputTokens) / 1_000_000.0 * pricing[1]
}

func (s *SessionStats) SetBalance(balance float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ReportedBalance = balance
	s.BalanceSetAt = time.Now().UTC().Format(time.RFC3339)
}

// SessionStatsSnapshot is a copy-safe snapshot of session stats.
type SessionStatsSnapshot struct {
	TotalInputTokens  int64             `json:"total_input_tokens"`
	TotalOutputTokens int64             `json:"total_output_tokens"`
	TotalRequests     int64             `json:"total_requests"`
	EstimatedCostUSD  float64           `json:"estimated_cost_usd"`
	ReportedBalance   float64           `json:"reported_balance_usd,omitempty"`
	BalanceSetAt      string            `json:"balance_set_at,omitempty"`
	ContextTokens     int               `json:"context_tokens"`
	ContextWindow     int               `json:"context_window"`
	MessageCount      int               `json:"message_count"`
	Build             map[string]string `json:"build,omitempty"`
}

func (s *SessionStats) Snapshot() SessionStatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionStatsSnapshot{
		TotalInputTokens:  s.TotalInputTokens,
		TotalOutputTokens: s.TotalOutputTokens,
		TotalRequests:     s.TotalRequests,
		EstimatedCostUSD:  s.EstimatedCostUSD,
		ReportedBalance:   s.ReportedBalance,
		BalanceSetAt:      s.BalanceSetAt,
	}
}

// NewServer creates a new API server.
func NewServer(address string, port int, loop *agent.Loop, rtr *router.Router, logger *slog.Logger) *Server {
	return &Server{
		address: address,
		port:    port,
		loop:    loop,
		router:  rtr,
		logger:  logger,
		stats:   &SessionStats{},
	}
}

// SetCheckpointer configures the checkpointer for checkpoint API endpoints.
func (s *Server) SetCheckpointer(cp *checkpoint.Checkpointer) {
	s.checkpointer = cp
}

// SetMemoryStore configures the memory store for history API endpoints.
func (s *Server) SetMemoryStore(ms *memory.SQLiteStore) {
	s.memoryStore = ms
}

// SetArchiveStore configures the archive store for archive API endpoints.
func (s *Server) SetArchiveStore(as *memory.ArchiveStore) {
	s.archiveStore = as
}

// Start begins serving HTTP requests.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// OpenAI-compatible endpoints
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("GET /v1/models", s.handleModels)

	// Simplified chat endpoint (easier testing)
	mux.HandleFunc("POST /v1/chat", s.handleSimpleChat)

	// Health endpoints
	mux.HandleFunc("GET /v1/version", s.handleVersion)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /", s.handleRoot)

	// Router introspection endpoints
	mux.HandleFunc("GET /v1/router/stats", s.handleRouterStats)
	mux.HandleFunc("GET /v1/router/audit", s.handleRouterAudit)
	mux.HandleFunc("GET /v1/router/explain/{requestId}", s.handleRouterExplain)

	// Checkpoint endpoints
	mux.HandleFunc("POST /v1/checkpoint", s.handleCheckpointCreate)
	mux.HandleFunc("GET /v1/checkpoints", s.handleCheckpointList)
	mux.HandleFunc("GET /v1/checkpoint/{id}", s.handleCheckpointGet)
	mux.HandleFunc("DELETE /v1/checkpoint/{id}", s.handleCheckpointDelete)
	mux.HandleFunc("POST /v1/checkpoint/{id}/restore", s.handleCheckpointRestore)

	// History endpoints
	mux.HandleFunc("GET /v1/conversations", s.handleConversationList)
	mux.HandleFunc("GET /v1/conversations/{id}", s.handleConversationGet)
	mux.HandleFunc("GET /v1/tools/calls", s.handleToolCalls)
	mux.HandleFunc("GET /v1/tools/stats", s.handleToolStats)

	// Session stats
	mux.HandleFunc("GET /v1/session/stats", s.handleSessionStats)
	mux.HandleFunc("POST /v1/session/balance", s.handleSetBalance)
	mux.HandleFunc("POST /v1/session/reset", s.handleSessionReset)
	mux.HandleFunc("POST /v1/session/compact", s.handleSessionCompact)
	mux.HandleFunc("GET /v1/session/history", s.handleSessionHistory)

	// Archive endpoints
	mux.HandleFunc("GET /v1/archive/sessions", s.handleArchiveSessions)
	mux.HandleFunc("GET /v1/archive/sessions/{id}", s.handleArchiveSessionGet)
	mux.HandleFunc("GET /v1/archive/sessions/{id}/export", s.handleArchiveSessionExport)
	mux.HandleFunc("GET /v1/archive/search", s.handleArchiveSearch)
	mux.HandleFunc("GET /v1/archive/messages", s.handleArchiveMessages)
	mux.HandleFunc("GET /v1/archive/stats", s.handleArchiveStats)

	// Chat web UI
	web.RegisterRoutes(mux)

	// Note: Ollama-compatible API is served on a separate port via OllamaServer
	// when ollama_api.enabled is true in config. Use RegisterOllamaRoutes()
	// only if you need single-port operation.

	s.server = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", s.address, s.port),
		Handler:      s.withLogging(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second, // Long for streaming responses
	}

	addr := s.address
	if addr == "" {
		addr = "0.0.0.0"
	}
	s.logger.Info("starting API server", "address", addr, "port", s.port)
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
	writeJSON(w, map[string]string{
		"name":    "Thane",
		"version": buildinfo.Version,
		"status":  "ok",
	}, s.logger)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, buildinfo.RuntimeInfo(), s.logger)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]string{"status": "healthy"}, s.logger)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	// OpenAI-compatible models list
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"object": "list",
		"data": []map[string]any{
			{
				"id":       "thane",
				"object":   "model",
				"created":  time.Now().Unix(),
				"owned_by": "thane",
			},
		},
	}, s.logger)
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

	// Record usage stats
	s.stats.Record(resp.Model, resp.InputTokens, resp.OutputTokens)

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
	writeJSON(w, completion, s.logger)
}

// SimpleChatRequest is a minimal chat request for easy testing.
type SimpleChatRequest struct {
	Message        string `json:"message"`
	ConversationID string `json:"conversation_id,omitempty"`
}

// SimpleChatResponse is a minimal chat response.
type SimpleChatResponse struct {
	Response       string   `json:"response"`
	Model          string   `json:"model"`
	ConversationID string   `json:"conversation_id"`
	ToolCalls      []string `json:"tool_calls,omitempty"` // Tool names used
}

// handleSimpleChat provides a simplified chat interface for testing.
// POST /v1/chat {"message": "turn on the lights"}
func (s *Server) handleSimpleChat(w http.ResponseWriter, r *http.Request) {
	var req SimpleChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Message == "" {
		s.errorResponse(w, http.StatusBadRequest, "message is required")
		return
	}

	convID := req.ConversationID
	if convID == "" {
		convID = uuid.New().String()
	}

	agentReq := &agent.Request{
		Messages: []agent.Message{
			{Role: "user", Content: req.Message},
		},
		ConversationID: convID,
	}

	resp, err := s.loop.Run(r.Context(), agentReq, nil)
	if err != nil {
		s.logger.Error("agent loop failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "agent error: "+err.Error())
		return
	}

	s.stats.Record(resp.Model, resp.InputTokens, resp.OutputTokens)

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, SimpleChatResponse{
		Response:       resp.Content,
		Model:          resp.Model,
		ConversationID: convID,
	}, s.logger)
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

	// Track if any tokens were streamed (greeting fast-path skips streaming)
	streamed := false

	// Get response controller for deadline management (Go 1.20+)
	rc := http.NewResponseController(w)

	// Stream callback sends tokens and keepalives during tool execution
	streamCallback := func(event agent.StreamEvent) {
		switch event.Kind {
		case agent.KindToken:
			streamed = true
			chunk := StreamChunk{
				ID:      completionID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   modelName,
				Choices: []StreamChoice{{
					Index: 0,
					Delta: StreamDelta{Content: event.Token},
				}},
			}
			s.writeSSE(w, chunk)
			flusher.Flush()

		case agent.KindToolCallStart, agent.KindToolCallDone:
			// Send SSE comment as keepalive to prevent write timeout
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}

		// Reset write deadline after every event to prevent timeout
		// during multi-iteration tool loops
		if err := rc.SetWriteDeadline(time.Now().Add(120 * time.Second)); err != nil {
			s.logger.Debug("failed to reset write deadline", "error", err)
		}
	}

	// Run agent with streaming
	resp, err := s.loop.Run(r.Context(), agentReq, streamCallback)
	if err != nil {
		s.logger.Error("agent loop failed", "error", err)
		// Can't change status code after streaming started, just close
		return
	}

	// If content was not streamed (e.g. greeting fast-path), emit it now
	if !streamed && resp.Content != "" {
		streamCallback(agent.StreamEvent{Kind: agent.KindToken, Token: resp.Content})
	}

	// Record usage stats
	s.stats.Record(resp.Model, resp.InputTokens, resp.OutputTokens)

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
	data, err := json.Marshal(chunk)
	if err != nil {
		s.logger.Debug("failed to marshal SSE chunk", "error", err)
		return
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		s.logger.Debug("failed to write SSE chunk", "error", err)
	}
}

func (s *Server) errorResponse(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	writeJSON(w, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    code,
		},
	}, s.logger)
}

// Router introspection handlers

func (s *Server) handleRouterStats(w http.ResponseWriter, r *http.Request) {
	if s.router == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "router not configured")
		return
	}

	stats := s.router.GetStats()
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, stats, s.logger)
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
	writeJSON(w, map[string]any{
		"count":     len(decisions),
		"decisions": decisions,
	}, s.logger)
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
	writeJSON(w, decision, s.logger)
}

// Checkpoint handlers

type checkpointCreateRequest struct {
	Trigger string `json:"trigger,omitempty"` // defaults to "manual"
	Note    string `json:"note,omitempty"`
}

func (s *Server) handleCheckpointCreate(w http.ResponseWriter, r *http.Request) {
	if s.checkpointer == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "checkpointing not configured")
		return
	}

	var req checkpointCreateRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.errorResponse(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	trigger := checkpoint.TriggerManual
	if req.Trigger != "" {
		trigger = checkpoint.Trigger(req.Trigger)
	}

	cp, err := s.checkpointer.Create(trigger, req.Note)
	if err != nil {
		s.logger.Error("checkpoint create failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "failed to create checkpoint")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, cp, s.logger)
}

func (s *Server) handleCheckpointList(w http.ResponseWriter, r *http.Request) {
	if s.checkpointer == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "checkpointing not configured")
		return
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	checkpoints, err := s.checkpointer.List(limit)
	if err != nil {
		s.logger.Error("checkpoint list failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "failed to list checkpoints")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"count":       len(checkpoints),
		"checkpoints": checkpoints,
	}, s.logger)
}

func (s *Server) handleCheckpointGet(w http.ResponseWriter, r *http.Request) {
	if s.checkpointer == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "checkpointing not configured")
		return
	}

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid checkpoint id")
		return
	}

	cp, err := s.checkpointer.Get(id)
	if err != nil {
		s.logger.Error("checkpoint get failed", "error", err, "id", idStr)
		s.errorResponse(w, http.StatusNotFound, "checkpoint not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, cp, s.logger)
}

func (s *Server) handleCheckpointDelete(w http.ResponseWriter, r *http.Request) {
	if s.checkpointer == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "checkpointing not configured")
		return
	}

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid checkpoint id")
		return
	}

	if err := s.checkpointer.Delete(id); err != nil {
		s.logger.Error("checkpoint delete failed", "error", err, "id", idStr)
		s.errorResponse(w, http.StatusNotFound, "checkpoint not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCheckpointRestore(w http.ResponseWriter, r *http.Request) {
	if s.checkpointer == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "checkpointing not configured")
		return
	}

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid checkpoint id")
		return
	}

	if err := s.checkpointer.Restore(id); err != nil {
		s.logger.Error("checkpoint restore failed", "error", err, "id", idStr)
		s.errorResponse(w, http.StatusInternalServerError, "failed to restore checkpoint")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"status":  "restored",
		"id":      idStr,
		"message": "checkpoint restored successfully",
	}, s.logger)
}

// History endpoints

func (s *Server) handleConversationList(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "memory store not configured")
		return
	}

	convs := s.memoryStore.GetAllConversations()

	// Return summary without full message content
	type ConvSummary struct {
		ID           string    `json:"id"`
		MessageCount int       `json:"message_count"`
		CreatedAt    time.Time `json:"created_at"`
		UpdatedAt    time.Time `json:"updated_at"`
	}

	summaries := make([]ConvSummary, len(convs))
	for i, c := range convs {
		summaries[i] = ConvSummary{
			ID:           c.ID,
			MessageCount: len(c.Messages),
			CreatedAt:    c.CreatedAt,
			UpdatedAt:    c.UpdatedAt,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"conversations": summaries,
		"count":         len(summaries),
	}, s.logger)
}

func (s *Server) handleConversationGet(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "memory store not configured")
		return
	}

	id := r.PathValue("id")
	conv := s.memoryStore.GetConversation(id)
	if conv == nil {
		s.errorResponse(w, http.StatusNotFound, "conversation not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, conv, s.logger)
}

func (s *Server) handleToolCalls(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "memory store not configured")
		return
	}

	// Parse query params
	convID := r.URL.Query().Get("conversation_id")
	toolName := r.URL.Query().Get("tool")
	limitStr := r.URL.Query().Get("limit")

	limit := 50
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}

	var calls []memory.ToolCall
	if toolName != "" {
		calls = s.memoryStore.GetToolCallsByName(toolName, limit)
	} else if convID != "" {
		calls = s.memoryStore.GetToolCalls(convID, limit)
	} else {
		// Get all recent (no specific filter)
		calls = s.memoryStore.GetToolCalls("", limit)
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"tool_calls": calls,
		"count":      len(calls),
	}, s.logger)
}

func (s *Server) handleToolStats(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "memory store not configured")
		return
	}

	stats := s.memoryStore.ToolCallStats()

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, stats, s.logger)
}

func (s *Server) handleSessionStats(w http.ResponseWriter, r *http.Request) {
	snap := s.stats.Snapshot()

	// Enrich with context window info from memory
	memStats := s.loop.MemoryStats()
	if msgs, ok := memStats["messages"].(int); ok {
		snap.MessageCount = msgs
	}
	// Estimate context tokens from the "default" conversation
	snap.ContextTokens = s.loop.GetTokenCount("default")
	snap.ContextWindow = s.loop.GetContextWindow()
	snap.Build = buildinfo.RuntimeInfo()

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, snap, s.logger)
}

func (s *Server) handleSetBalance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Balance float64 `json:"balance_usd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	s.stats.SetBalance(req.Balance)
	s.logger.Info("balance updated", "balance_usd", req.Balance)
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{"status": "ok", "balance_usd": req.Balance}, s.logger)
}

func (s *Server) handleSessionReset(w http.ResponseWriter, r *http.Request) {
	if err := s.loop.ResetConversation("default"); err != nil {
		s.logger.Error("session reset failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "reset failed")
		return
	}
	s.logger.Info("session reset via API")
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{"status": "ok", "message": "conversation cleared"}, s.logger)
}

func (s *Server) handleSessionCompact(w http.ResponseWriter, r *http.Request) {
	if err := s.loop.TriggerCompaction(r.Context(), "default"); err != nil {
		s.logger.Error("compaction failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "compaction failed: "+err.Error())
		return
	}
	s.logger.Info("compaction triggered via API")
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{"status": "ok", "message": "conversation compacted"}, s.logger)
}

func (s *Server) handleSessionHistory(w http.ResponseWriter, r *http.Request) {
	messages := s.loop.GetHistory("default")

	// Filter to user/assistant messages only (skip system/tool)
	type historyMessage struct {
		Role      string `json:"role"`
		Content   string `json:"content"`
		Timestamp string `json:"timestamp"`
	}

	var filtered []historyMessage
	for _, m := range messages {
		if m.Role == "user" || m.Role == "assistant" {
			filtered = append(filtered, historyMessage{
				Role:      m.Role,
				Content:   m.Content,
				Timestamp: m.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{"messages": filtered}, s.logger)
}

// --- Archive endpoints ---

func (s *Server) handleArchiveSessions(w http.ResponseWriter, r *http.Request) {
	if s.archiveStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "archive not configured")
		return
	}

	convID := r.URL.Query().Get("conversation_id")
	limit := parseIntParam(r, "limit", 50)

	sessions, err := s.archiveStore.ListSessions(convID, limit)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "list sessions: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"sessions": sessions,
		"count":    len(sessions),
	}, s.logger)
}

func (s *Server) handleArchiveSessionGet(w http.ResponseWriter, r *http.Request) {
	if s.archiveStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "archive not configured")
		return
	}

	id := r.PathValue("id")

	sess, err := s.archiveStore.GetSession(id)
	if err != nil || sess == nil {
		s.errorResponse(w, http.StatusNotFound, "session not found")
		return
	}

	transcript, err := s.archiveStore.GetSessionTranscript(id)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "get transcript: "+err.Error())
		return
	}

	toolCalls, err := s.archiveStore.GetSessionToolCalls(id)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "get tool calls: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"session":    sess,
		"transcript": transcript,
		"tool_calls": toolCalls,
	}, s.logger)
}

func (s *Server) handleArchiveSessionExport(w http.ResponseWriter, r *http.Request) {
	if s.archiveStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "archive not configured")
		return
	}

	id := r.PathValue("id")
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "markdown"
	}

	switch format {
	case "markdown", "md":
		md, err := s.archiveStore.ExportSessionMarkdown(id)
		if err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "export: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"session-%s.md\"", id[:8]))
		fmt.Fprint(w, md)

	case "json":
		sess, err := s.archiveStore.GetSession(id)
		if err != nil || sess == nil {
			s.errorResponse(w, http.StatusNotFound, "session not found")
			return
		}
		transcript, err := s.archiveStore.GetSessionTranscript(id)
		if err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "get transcript: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"session-%s.json\"", id[:8]))
		writeJSON(w, map[string]any{
			"session":    sess,
			"transcript": transcript,
		}, s.logger)

	default:
		s.errorResponse(w, http.StatusBadRequest, "unsupported format: "+format+" (use markdown or json)")
	}
}

func (s *Server) handleArchiveSearch(w http.ResponseWriter, r *http.Request) {
	if s.archiveStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "archive not configured")
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		s.errorResponse(w, http.StatusBadRequest, "q parameter is required")
		return
	}

	opts := memory.SearchOptions{
		Query:          query,
		ConversationID: r.URL.Query().Get("conversation_id"),
		Limit:          parseIntParam(r, "limit", 10),
	}

	// Parse silence threshold
	if silenceStr := r.URL.Query().Get("silence"); silenceStr != "" {
		if d, err := time.ParseDuration(silenceStr); err == nil {
			opts.SilenceThreshold = d
		}
	}

	// Parse context=0 to disable context expansion
	if r.URL.Query().Get("context") == "0" {
		opts.NoContext = true
	}

	results, err := s.archiveStore.Search(opts)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "search: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"results": results,
		"count":   len(results),
		"query":   query,
	}, s.logger)
}

func (s *Server) handleArchiveMessages(w http.ResponseWriter, r *http.Request) {
	if s.archiveStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "archive not configured")
		return
	}

	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")

	if fromStr == "" || toStr == "" {
		s.errorResponse(w, http.StatusBadRequest, "from and to parameters are required (RFC3339)")
		return
	}

	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid from time: "+err.Error())
		return
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid to time: "+err.Error())
		return
	}

	convID := r.URL.Query().Get("conversation_id")
	limit := parseIntParam(r, "limit", 500)

	messages, err := s.archiveStore.GetMessagesByTimeRange(from, to, convID, limit)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "query: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"messages": messages,
		"count":    len(messages),
		"from":     fromStr,
		"to":       toStr,
	}, s.logger)
}

func (s *Server) handleArchiveStats(w http.ResponseWriter, r *http.Request) {
	if s.archiveStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "archive not configured")
		return
	}

	stats, err := s.archiveStore.Stats()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "stats: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, stats, s.logger)
}

func parseIntParam(r *http.Request, name string, defaultVal int) int {
	s := r.URL.Query().Get(name)
	if s == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return defaultVal
	}
	return n
}
