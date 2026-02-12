// Package api implements the Ollama-compatible HTTP API endpoints.
// This allows Thane to be used as a drop-in replacement for Ollama
// in Home Assistant's native Ollama integration.
//
// The Ollama-compatible API can be served either:
// - On a dedicated port via OllamaServer (recommended for HA integration)
// - Embedded in the main server via RegisterOllamaRoutes (for single-port setups)
package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
)

// OllamaChatRequest is the Ollama /api/chat request format.
type OllamaChatRequest struct {
	Model     string              `json:"model"`
	Messages  []OllamaChatMessage `json:"messages"`
	Stream    *bool               `json:"stream,omitempty"`
	Options   map[string]any      `json:"options,omitempty"`
	Format    string              `json:"format,omitempty"`
	Tools     []map[string]any    `json:"tools,omitempty"`
	Think     bool                `json:"think,omitempty"`
	KeepAlive string              `json:"keep_alive,omitempty"`
}

// OllamaChatMessage is the Ollama message format.
type OllamaChatMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

// OllamaChatResponse is the Ollama /api/chat response format.
type OllamaChatResponse struct {
	Model              string            `json:"model"`
	CreatedAt          string            `json:"created_at"`
	Message            OllamaChatMessage `json:"message"`
	Done               bool              `json:"done"`
	DoneReason         string            `json:"done_reason,omitempty"`
	TotalDuration      int64             `json:"total_duration,omitempty"`
	LoadDuration       int64             `json:"load_duration,omitempty"`
	PromptEvalCount    int               `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64             `json:"prompt_eval_duration,omitempty"`
	EvalCount          int               `json:"eval_count,omitempty"`
	EvalDuration       int64             `json:"eval_duration,omitempty"`
}

// OllamaTagsResponse is the Ollama /api/tags response format.
type OllamaTagsResponse struct {
	Models []OllamaModel `json:"models"`
}

// OllamaModel represents a model in the tags response.
type OllamaModel struct {
	Name       string            `json:"name"`
	Model      string            `json:"model"`
	ModifiedAt string            `json:"modified_at"`
	Size       int64             `json:"size"`
	Digest     string            `json:"digest"`
	Details    OllamaModelDetail `json:"details"`
}

// OllamaModelDetail contains model details.
type OllamaModelDetail struct {
	ParentModel       string   `json:"parent_model"`
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
}

// OllamaVersionResponse is the Ollama /api/version response.
type OllamaVersionResponse struct {
	Version string `json:"version"`
}

// RegisterOllamaRoutes adds Ollama-compatible API endpoints to the mux.
// Use this for single-port setups. For dual-port, use OllamaServer instead.
func (s *Server) RegisterOllamaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		handleOllamaChatShared(w, r, s.loop, s.logger)
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		handleOllamaTagsShared(w, r, s.logger)
	})
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		handleOllamaVersionShared(w, r, s.logger)
	})
}

// handleOllamaChatShared handles the Ollama /api/chat endpoint.
// This is a shared implementation used by both OllamaServer and embedded routes.
func handleOllamaChatShared(w http.ResponseWriter, r *http.Request, loop *agent.Loop, logger *slog.Logger) {
	start := time.Now()

	// Capture raw request for parsing; headers/body logged at debug level only
	headers := make(map[string]string)
	for k, v := range r.Header {
		headers[k] = v[0]
		if len(v) > 1 {
			headers[k] = fmt.Sprintf("%v", v)
		}
	}

	rawBody, err := captureBody(r)
	if err != nil {
		ollamaError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	var req OllamaChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ollamaError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Derive real client IP from reverse proxy headers
	remoteIP := r.RemoteAddr
	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		remoteIP = xri
	} else if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		remoteIP = xff
	}

	logger.Info("ollama chat request received",
		"remote_addr", r.RemoteAddr,
		"remote_ip", remoteIP,
		"user_agent", r.Header.Get("User-Agent"),
		"model", req.Model,
		"messages", len(req.Messages),
		"stream", req.Stream,
	)
	logger.Debug("ollama chat request details",
		"headers", headers,
		"body_len", len(rawBody),
	)

	// Sanitize: strip HA tools and instructions, extract area context
	areaContext := sanitizeHARequest(&req, logger)
	_ = areaContext // TODO: pass to agent for room-aware responses

	// Convert Ollama messages to agent messages
	messages := make([]agent.Message, len(req.Messages))
	for i, m := range req.Messages {
		messages[i] = agent.Message{
			Role:    m.Role,
			Content: m.Content,
		}
	}

	// Map model name: "thane:latest" or "thane" should use default model
	// Don't pass our fake model name through to the real LLM
	model := req.Model
	if model == "" || model == "thane" || model == "thane:latest" {
		model = "" // Empty = use Thane's configured default
	}

	agentReq := &agent.Request{
		Messages: messages,
		Model:    model,
	}

	// Check if streaming was requested. For Ollama compatibility, a nil stream defaults to true.
	stream := true
	if req.Stream != nil {
		stream = *req.Stream
	}
	
	if stream {
		handleOllamaStreamingChatShared(w, r, agentReq, start, loop, logger)
		return
	}

	// Non-streaming response
	resp, err := loop.Run(r.Context(), agentReq, nil)
	if err != nil {
		logger.Error("agent loop failed", "error", err)
		ollamaError(w, http.StatusInternalServerError, "agent error")
		return
	}

	duration := time.Since(start)

	ollamaResp := OllamaChatResponse{
		Model:     resp.Model,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Message: OllamaChatMessage{
			Role:    "assistant",
			Content: resp.Content,
		},
		Done:          true,
		DoneReason:    "stop",
		TotalDuration: duration.Nanoseconds(),
		EvalDuration:  duration.Nanoseconds(),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(ollamaResp); err != nil {
		logger.Debug("failed to encode ollama response", "error", err)
	}
}

// handleOllamaStreamingChatShared handles streaming chat responses in Ollama format.
func handleOllamaStreamingChatShared(w http.ResponseWriter, r *http.Request, req *agent.Request, start time.Time, loop *agent.Loop, logger *slog.Logger) {
	// Set headers for streaming
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		ollamaError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	model := "thane"
	if req.Model != "" {
		model = req.Model
	}

	// Buffer to detect tool calls at the start of streaming
	// Note: Tool call detection relies on KindToolCallStart events which are emitted
	// by the agent loop before any tool-related tokens. This prevents JSON leakage
	// in the stream. If the LLM generates text-based tool calls (not using the proper
	// tool syntax), those would stream as regular text - but that's expected behavior.
	var buffer []agent.StreamEvent
	var hasToolCalls bool
	var streaming bool

	// Create stream callback that buffers initially, then streams if no tool calls
	streamCallback := func(event agent.StreamEvent) {
		// If we've already detected tool calls, stop processing events
		if hasToolCalls {
			return
		}
		
		if !streaming {
			// Still buffering - check for tool calls
			if event.Kind == agent.KindToolCallStart {
				hasToolCalls = true
				buffer = nil // Clear buffer to avoid unnecessary memory usage
				// Stop buffering and fall back to non-streaming
				return
			}

			// Only buffer token events
			if event.Kind == agent.KindToken {
				buffer = append(buffer, event)
			}

			// Start streaming after we get some content and no tool calls
			// Use a smaller threshold to handle short responses
			if event.Kind == agent.KindToken && len(buffer) >= 2 && !hasToolCalls {
				streaming = true
				// Flush buffered content
				for _, bufferedEvent := range buffer {
					chunk := OllamaChatResponse{
						Model:     model,
						CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
						Message: OllamaChatMessage{
							Role:    "assistant",
							Content: bufferedEvent.Token,
						},
						Done: false,
					}
					data, _ := json.Marshal(chunk)
					fmt.Fprintf(w, "%s\n", data)
					flusher.Flush()
				}
				buffer = nil // Clear buffer
			}
		} else {
			// We're streaming - send tokens directly
			if event.Kind == agent.KindToken {
				chunk := OllamaChatResponse{
					Model:     model,
					CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
					Message: OllamaChatMessage{
						Role:    "assistant",
						Content: event.Token,
					},
					Done: false,
				}
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "%s\n", data)
				flusher.Flush()
			}
		}
	}

	// Run agent with streaming callback
	resp, err := loop.Run(r.Context(), req, streamCallback)
	if err != nil {
		logger.Error("streaming failed", "error", err)
		// Send error as final message
		errResp := OllamaChatResponse{
			Model:     model,
			CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Message: OllamaChatMessage{
				Role:    "assistant",
				Content: fmt.Sprintf("Error: %v", err),
			},
			Done:       true,
			DoneReason: "error",
		}
		data, _ := json.Marshal(errResp)
		fmt.Fprintf(w, "%s\n", data)
		flusher.Flush()
		return
	}

	// If we detected tool calls and buffered everything, send it now
	if hasToolCalls && !streaming {
		// Send the complete response as a single chunk
		chunk := OllamaChatResponse{
			Model:     model,
			CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Message: OllamaChatMessage{
				Role:    "assistant",
				Content: resp.Content,
			},
			Done: false,
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "%s\n", data)
		flusher.Flush()
	}

	// If we never started streaming (very short response), send buffered tokens now
	if !streaming && !hasToolCalls && len(buffer) > 0 {
		for _, bufferedEvent := range buffer {
			chunk := OllamaChatResponse{
				Model:     model,
				CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
				Message: OllamaChatMessage{
					Role:    "assistant",
					Content: bufferedEvent.Token,
				},
				Done: false,
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "%s\n", data)
			flusher.Flush()
		}
	}

	// Send final message with stats
	duration := time.Since(start)
	final := OllamaChatResponse{
		Model:     resp.Model,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Message: OllamaChatMessage{
			Role:    "assistant",
			Content: "",
		},
		Done:          true,
		DoneReason:    "stop",
		TotalDuration: duration.Nanoseconds(),
		EvalDuration:  duration.Nanoseconds(),
	}
	data, _ := json.Marshal(final)
	fmt.Fprintf(w, "%s\n", data)
	flusher.Flush()
}

// handleOllamaTagsShared returns the list of available models.
func handleOllamaTagsShared(w http.ResponseWriter, r *http.Request, logger *slog.Logger) {
	// Return Thane as the only available model
	resp := OllamaTagsResponse{
		Models: []OllamaModel{
			{
				Name:       "thane:latest",
				Model:      "thane:latest",
				ModifiedAt: time.Now().UTC().Format(time.RFC3339),
				Size:       0,
				Digest:     "thane-autonomous-agent",
				Details: OllamaModelDetail{
					ParentModel:       "",
					Format:            "thane",
					Family:            "thane",
					Families:          []string{"thane"},
					ParameterSize:     "autonomous",
					QuantizationLevel: "native",
				},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Debug("failed to encode tags response", "error", err)
	}
}

// handleOllamaVersionShared returns the Ollama-compatible version.
func handleOllamaVersionShared(w http.ResponseWriter, r *http.Request, logger *slog.Logger) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(OllamaVersionResponse{
		Version: "0.1.1", // Thane version
	}); err != nil {
		logger.Debug("failed to encode version response", "error", err)
	}
}

// ollamaError sends an error response in a format Ollama clients expect.
// Errors writing the response are intentionally not logged - if the client
// disconnected, there's nothing actionable we can do.
func ollamaError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	// Best-effort write - client may have disconnected
	_, _ = w.Write([]byte(`{"error":"` + message + `"}`))
}

// sanitizeHARequest strips HA-provided tools and instructions, keeping only
// what Thane needs. HA is the dumb pipe; Thane is the brain.
func sanitizeHARequest(req *OllamaChatRequest, logger *slog.Logger) (areaContext string) {
	// Log and strip HA tools (we use our own)
	if len(req.Tools) > 0 {
		toolNames := make([]string, 0, len(req.Tools))
		for _, tool := range req.Tools {
			if fn, ok := tool["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					toolNames = append(toolNames, name)
				}
			}
		}
		logger.Info("HA tools stripped",
			"count", len(req.Tools),
			"tools", toolNames,
		)
		req.Tools = nil
	}

	// Clean up assistant messages that have JSON tool calls as text
	// (from previous text-based tool call parsing that leaked to HA)
	// Check both unescaped and escaped JSON prefixes
	for i, msg := range req.Messages {
		if msg.Role == "assistant" {
			content := msg.Content
			// Check for JSON tool call patterns (escaped or unescaped)
			hasToolJSON := strings.HasPrefix(content, "{\"name\":") ||
				strings.HasPrefix(content, "{\\\"name\\\":") ||
				strings.Contains(content, "\"name\": \"find_entity\"") ||
				strings.Contains(content, "\"name\": \"call_service\"")

			if hasToolJSON {
				// Find where JSON objects end and keep only the trailing text
				cleaned := stripLeadingJSON(content)
				if cleaned != content {
					req.Messages[i].Content = cleaned
					logger.Info("stripped JSON from assistant message",
						"original_len", len(content),
						"cleaned", cleaned,
					)
				}
			}
		}
	}

	// Extract area context from system message before stripping
	// Look for "You are in area X (floor Y)"
	for i, msg := range req.Messages {
		if msg.Role == "system" {
			// Extract area context if present
			if idx := strings.Index(msg.Content, "You are in area "); idx != -1 {
				end := strings.Index(msg.Content[idx:], "\n")
				if end == -1 {
					end = len(msg.Content) - idx
				}
				areaContext = msg.Content[idx : idx+end]
				logger.Info("area context extracted", "area", areaContext)
			}

			// For now, strip the entire HA system message
			// Thane will inject its own via talents
			req.Messages = append(req.Messages[:i], req.Messages[i+1:]...)
			logger.Info("HA system message stripped")
			break
		}
	}

	return areaContext
}

// stripLeadingJSON removes JSON objects from the beginning of a string,
// returning any trailing text. Handles multiple consecutive JSON objects.
func stripLeadingJSON(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "{") {
		end := findJSONEnd(s)
		if end <= 0 {
			break
		}
		s = strings.TrimSpace(s[end:])
	}
	return s
}

// findJSONEnd finds the end of a JSON object in a string.
func findJSONEnd(s string) int {
	if !strings.HasPrefix(s, "{") {
		return -1
	}
	depth := 0
	inString := false
	escape := false
	for i, c := range s {
		if escape {
			escape = false
			continue
		}
		if c == '\\' {
			escape = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// Note: captureBody is defined in debug_request.go
