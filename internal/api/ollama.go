// Package api implements the Ollama-compatible HTTP API endpoints.
// This allows Thane to be used as a drop-in replacement for Ollama
// in Home Assistant's native Ollama integration.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
)

// OllamaChatRequest is the Ollama /api/chat request format.
type OllamaChatRequest struct {
	Model    string               `json:"model"`
	Messages []OllamaChatMessage  `json:"messages"`
	Stream   *bool                `json:"stream,omitempty"`
	Options  map[string]any       `json:"options,omitempty"`
	Format   string               `json:"format,omitempty"`
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
func (s *Server) RegisterOllamaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/chat", s.handleOllamaChat)
	mux.HandleFunc("GET /api/tags", s.handleOllamaTags)
	mux.HandleFunc("GET /api/version", s.handleOllamaVersion)
}

// handleOllamaChat handles the Ollama /api/chat endpoint.
func (s *Server) handleOllamaChat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Capture and log raw request for debugging
	rawBody, err := captureBody(r)
	if err != nil {
		s.ollamaError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	s.logger.Info("ollama chat request received", "raw_body", string(rawBody))

	var req OllamaChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.ollamaError(w, http.StatusBadRequest, "invalid request body")
		return
	}

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

	// Check if streaming is requested (default true for Ollama)
	streaming := true
	if req.Stream != nil {
		streaming = *req.Stream
	}

	if streaming {
		s.handleOllamaStreamingChat(w, r, agentReq, start)
		return
	}

	// Non-streaming response
	resp, err := s.loop.Run(r.Context(), agentReq, nil)
	if err != nil {
		s.logger.Error("agent loop failed", "error", err)
		s.ollamaError(w, http.StatusInternalServerError, "agent error")
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
	_ = json.NewEncoder(w).Encode(ollamaResp)
}

// handleOllamaStreamingChat handles streaming chat responses in Ollama format.
func (s *Server) handleOllamaStreamingChat(w http.ResponseWriter, r *http.Request, req *agent.Request, start time.Time) {
	// Set headers for streaming
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.ollamaError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	model := "thane"
	if req.Model != "" {
		model = req.Model
	}

	// Create stream callback that writes Ollama-format chunks
	streamCallback := func(token string) {
		chunk := OllamaChatResponse{
			Model:     model,
			CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Message: OllamaChatMessage{
				Role:    "assistant",
				Content: token,
			},
			Done: false,
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "%s\n", data)
		flusher.Flush()
	}

	// Run agent with streaming callback
	resp, err := s.loop.Run(r.Context(), req, streamCallback)
	if err != nil {
		s.logger.Error("streaming failed", "error", err)
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

// handleOllamaTags returns the list of available models.
func (s *Server) handleOllamaTags(w http.ResponseWriter, r *http.Request) {
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
	_ = json.NewEncoder(w).Encode(resp)
}

// handleOllamaVersion returns the Ollama-compatible version.
func (s *Server) handleOllamaVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(OllamaVersionResponse{
		Version: "0.1.0", // Thane version
	})
}

// ollamaError sends an error response in a format Ollama clients expect.
func (s *Server) ollamaError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": message,
	})
}
