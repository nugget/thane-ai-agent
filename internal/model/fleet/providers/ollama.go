// Package providers implements concrete model runner integrations.
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/httpkit"
)

// OllamaClient is a client for the Ollama API.
type OllamaClient struct {
	baseURL    string
	httpClient *http.Client
	logger     *slog.Logger
	watcher    llm.ReadyWatcher // set via SetWatcher for health status
}

// SetWatcher sets the connection watcher for health status queries.
func (c *OllamaClient) SetWatcher(w llm.ReadyWatcher) {
	c.watcher = w
}

// IsReady reports whether Ollama is currently reachable.
// Returns true if no watcher is configured (backward compatible).
func (c *OllamaClient) IsReady() bool {
	if c.watcher == nil {
		return true
	}
	return c.watcher.IsReady()
}

// NewOllamaClient creates a new Ollama client.
func NewOllamaClient(baseURL string, logger *slog.Logger) *OllamaClient {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if logger == nil {
		logger = slog.Default()
	}
	// Large local models can take significant time before sending headers
	// (loading, thinking). Override the default 15s ResponseHeaderTimeout.
	t := httpkit.NewTransport()
	t.ResponseHeaderTimeout = 5 * time.Minute

	return &OllamaClient{
		baseURL: baseURL,
		logger:  logger.With("provider", "ollama"),
		httpClient: httpkit.NewClient(
			httpkit.WithTimeout(5*time.Minute),
			httpkit.WithTransport(t),
			httpkit.WithRetry(3, 2*time.Second),
			httpkit.WithLogger(logger),
		),
	}
}

// SetLogger rebinds the request-level logger. See AnthropicClient.SetLogger
// for the late-bind rationale; the same caveat about httpkit retries applies.
//
// Not safe to call concurrently with in-flight requests; intended to be
// invoked once during init.
func (c *OllamaClient) SetLogger(logger *slog.Logger) {
	if c == nil || logger == nil {
		return
	}
	c.logger = logger.With("provider", "ollama")
}

// Logger returns the request-level logger. Exposed for tests and
// late-bind verification — production callers should not depend on it.
func (c *OllamaClient) Logger() *slog.Logger {
	if c == nil {
		return nil
	}
	return c.logger
}

// ChatRequest is the request format for Ollama chat API.
type ChatRequest struct {
	Model    string           `json:"model"`
	Messages []ollamaMessage  `json:"messages"`
	Stream   bool             `json:"stream"`
	Tools    []map[string]any `json:"tools,omitempty"`
	Options  *Options         `json:"options,omitempty"`
}

// ollamaMessage is the Ollama wire format for chat messages. Ollama
// accepts images as a flat array of base64 strings alongside each
// message, unlike Anthropic which uses typed content blocks.
type ollamaMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []llm.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Images     []string       `json:"images,omitempty"`
}

// toOllamaMessages converts internal Messages to Ollama wire format,
// extracting [ImageContent] into the flat base64 array Ollama expects.
func toOllamaMessages(msgs []llm.Message) []ollamaMessage {
	out := make([]ollamaMessage, len(msgs))
	for i, m := range msgs {
		om := ollamaMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
		}
		for _, img := range m.Images {
			om.Images = append(om.Images, img.Data)
		}
		out[i] = om
	}
	return out
}

// Options are model parameters.
type Options struct {
	Temperature float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

// OllamaModelDetails contains model metadata returned by /api/tags.
type OllamaModelDetails struct {
	Format            string   `json:"format,omitempty"`
	Family            string   `json:"family,omitempty"`
	Families          []string `json:"families,omitempty"`
	ParameterSize     string   `json:"parameter_size,omitempty"`
	QuantizationLevel string   `json:"quantization_level,omitempty"`
}

// OllamaModelInfo describes a single model discovered from an Ollama
// resource inventory.
type OllamaModelInfo struct {
	Name       string             `json:"name"`
	Digest     string             `json:"digest,omitempty"`
	Size       int64              `json:"size,omitempty"`
	ModifiedAt string             `json:"modified_at,omitempty"`
	Details    OllamaModelDetails `json:"details,omitempty"`
}

// ollamaWireResponse is the raw JSON response from Ollama's /api/chat endpoint.
// This is a deserialization target only — convert to ChatResponse for internal use.
type ollamaWireResponse struct {
	Model              string      `json:"model"`
	CreatedAt          string      `json:"created_at"`
	Message            llm.Message `json:"message"`
	Done               bool        `json:"done"`
	TotalDuration      int64       `json:"total_duration,omitempty"`
	LoadDuration       int64       `json:"load_duration,omitempty"`
	PromptEvalCount    int         `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64       `json:"prompt_eval_duration,omitempty"`
	EvalCount          int         `json:"eval_count,omitempty"`
	EvalDuration       int64       `json:"eval_duration,omitempty"`
}

// toChatResponse converts an Ollama wire response to the internal ChatResponse type.
func (w *ollamaWireResponse) toChatResponse() *llm.ChatResponse {
	createdAt, _ := time.Parse(time.RFC3339Nano, w.CreatedAt)
	return &llm.ChatResponse{
		Model:         w.Model,
		CreatedAt:     createdAt,
		Message:       w.Message,
		Done:          w.Done,
		InputTokens:   w.PromptEvalCount,
		OutputTokens:  w.EvalCount,
		TotalDuration: time.Duration(w.TotalDuration),
		LoadDuration:  time.Duration(w.LoadDuration),
		EvalDuration:  time.Duration(w.EvalDuration),
	}
}

// Chat sends a chat completion request to Ollama.
func (c *OllamaClient) Chat(ctx context.Context, model string, messages []llm.Message, tools []map[string]any) (*llm.ChatResponse, error) {
	return c.ChatStream(ctx, model, messages, tools, nil)
}

// ChatStream sends a streaming chat request to Ollama.
// If callback is non-nil, tokens are streamed to it.
func (c *OllamaClient) ChatStream(ctx context.Context, model string, messages []llm.Message, tools []map[string]any, callback llm.StreamCallback) (*llm.ChatResponse, error) {
	stream := callback != nil

	c.logger.Debug("preparing request",
		"model", model,
		"messages", len(messages),
		"tools", len(tools),
		"stream", stream,
	)

	req := ChatRequest{
		Model:    model,
		Messages: toOllamaMessages(messages),
		Stream:   stream,
		Tools:    tools,
	}

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	c.logger.Log(ctx, llm.LevelTrace, "request payload", "json", string(jsonData))

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/chat", bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody := httpkit.ReadErrorBody(resp.Body, 4096)
		c.logger.Error("API error", "status", resp.StatusCode, "body", errBody)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, errBody)
	}

	// Extract valid tool names for validation
	validToolNames := extractToolNames(tools)

	if !stream {
		// Non-streaming: single JSON response
		var wire ollamaWireResponse
		if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		chatResp := wire.toChatResponse()

		c.logger.Debug("response received",
			"model", chatResp.Model,
			"input_tokens", chatResp.InputTokens,
			"output_tokens", chatResp.OutputTokens,
			"total_duration", chatResp.TotalDuration,
			"tool_calls", len(chatResp.Message.ToolCalls),
		)
		c.logger.Log(ctx, llm.LevelTrace, "response content", "content", chatResp.Message.Content)

		// Try to parse text-based tool calls if no native tool_calls
		if len(chatResp.Message.ToolCalls) == 0 && chatResp.Message.Content != "" {
			if parsed := parseTextToolCalls(chatResp.Message.Content, validToolNames); len(parsed) > 0 {
				c.logger.Debug("parsed text-based tool calls", "count", len(parsed))
				chatResp.Message.ToolCalls = parsed
				chatResp.Message.Content = "" // Clear content since it was a tool call
			} else if looksLikeHallucinatedToolCall(chatResp.Message.Content) {
				c.logger.Warn("suppressed hallucinated tool call",
					"content", chatResp.Message.Content)
				chatResp.Message.Content = ""
			}
		}
		if chatResp.Message.Content != "" {
			chatResp.Message.Content = stripTrailingToolCallJSON(chatResp.Message.Content, validToolNames)
		}
		return chatResp, nil
	}

	// Streaming: read newline-delimited JSON
	var finalResp *llm.ChatResponse
	var toolCalls []llm.ToolCall
	var contentBuilder strings.Builder
	toolCallBufferFlushed := false // tracks whether we've started streaming to client
	decoder := json.NewDecoder(resp.Body)

	for {
		var wire ollamaWireResponse
		if err := decoder.Decode(&wire); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode stream chunk: %w", err)
		}

		// Accumulate content.
		// When tools are available, buffer tokens that look like they
		// might be text-based tool calls (starting with '{' or '<tool_call>')
		// so we don't stream raw JSON to the client prematurely.
		if wire.Message.Content != "" {
			contentBuilder.WriteString(wire.Message.Content)
			if callback != nil {
				accumulated := contentBuilder.String()
				if len(tools) > 0 && !toolCallBufferFlushed && looksLikeToolCall(accumulated) {
					// Hold back — might be a text-based tool call
				} else {
					// Flush any buffered content + this token
					if !toolCallBufferFlushed && contentBuilder.Len() > len(wire.Message.Content) {
						// First flush: send everything accumulated so far
						callback(llm.StreamEvent{Kind: llm.KindToken, Token: accumulated})
					} else {
						callback(llm.StreamEvent{Kind: llm.KindToken, Token: wire.Message.Content})
					}
					toolCallBufferFlushed = true
				}
			}
		}

		// Tool calls come in the final message
		if len(wire.Message.ToolCalls) > 0 {
			toolCalls = wire.Message.ToolCalls
		}

		// Capture final metadata
		if wire.Done {
			finalResp = wire.toChatResponse()
			finalResp.Message.Content = contentBuilder.String()
			finalResp.Message.ToolCalls = toolCalls
			break
		}
	}

	if finalResp == nil {
		c.logger.Debug("stream ended without done marker, synthesizing response")
		finalResp = &llm.ChatResponse{Model: model, Done: true}
		finalResp.Message.Content = contentBuilder.String()
		finalResp.Message.ToolCalls = toolCalls
	}

	c.logger.Debug("stream complete",
		"model", finalResp.Model,
		"input_tokens", finalResp.InputTokens,
		"output_tokens", finalResp.OutputTokens,
		"total_duration", finalResp.TotalDuration,
		"content_len", len(finalResp.Message.Content),
		"tool_calls", len(finalResp.Message.ToolCalls),
	)
	c.logger.Log(ctx, llm.LevelTrace, "stream final content", "content", finalResp.Message.Content)

	// Try to parse text-based tool calls if no native tool_calls
	if len(finalResp.Message.ToolCalls) == 0 && finalResp.Message.Content != "" {
		if parsed := parseTextToolCalls(finalResp.Message.Content, validToolNames); len(parsed) > 0 {
			c.logger.Debug("parsed text-based tool calls from stream", "count", len(parsed))
			finalResp.Message.ToolCalls = parsed
			finalResp.Message.Content = "" // Clear content since it was a tool call
		} else if looksLikeHallucinatedToolCall(finalResp.Message.Content) {
			c.logger.Warn("suppressed hallucinated tool call from stream",
				"content", finalResp.Message.Content)
			finalResp.Message.Content = ""
		}
	}

	// Strip trailing tool-call JSON from mixed prose+JSON responses.
	// Models sometimes answer and then append a raw tool call at the end.
	if finalResp.Message.Content != "" {
		finalResp.Message.Content = stripTrailingToolCallJSON(finalResp.Message.Content, validToolNames)
	}

	return finalResp, nil
}

// extractToolNames extracts tool names from the tools definition.
// Tools are expected to be in OpenAI/Ollama format with function.name.
func extractToolNames(tools []map[string]any) []string {
	return llm.ExtractToolNames(tools)
}

// looksLikeToolCall checks if accumulated stream content might be a text-based
// tool call. Used to buffer streaming output until we can determine whether
// the model is emitting a tool call as text or actual prose.
// looksLikeHallucinatedToolCall checks if content is JSON with "name" and "arguments"
// stripTrailingToolCallJSON removes JSON tool call objects appended to the end
// of prose content. Returns the cleaned prose (or original if no trailing JSON found).
func stripTrailingToolCallJSON(content string, validTools []string) string {
	return llm.StripTrailingToolCallText(content, validTools, llm.DefaultToolCallTextProfile())
}

// looksLikeHallucinatedToolCall checks if content is JSON with "name" and "arguments"
// fields — the shape of a tool call — but wasn't matched by parseTextToolCalls
// (meaning the tool name is invalid). This is a hallucinated tool call that should
// be suppressed rather than shown to the user.
func looksLikeHallucinatedToolCall(content string) bool {
	return llm.LooksLikeHallucinatedToolCall(content, llm.DefaultToolCallTextProfile())
}

func looksLikeToolCall(content string) bool {
	return llm.LooksLikeTextToolCall(content, llm.DefaultToolCallTextProfile())
}

// parseTextToolCalls attempts to extract tool calls from content text.
// Many models output tool calls as JSON in the content rather than using
// the native tool_calls field. This function handles common formats:
// - Raw JSON object: {"name": "...", "arguments": {...}}
// - JSON array: [{"name": "...", "arguments": {...}}]
// - Tagged: <tool_call>...</tool_call>
//
// If validTools is non-empty, only tool calls with names in that list are returned.
// This prevents false positives when models output JSON that happens to have
// name/arguments fields but isn't meant to be a tool call.
func parseTextToolCalls(content string, validTools []string) []llm.ToolCall {
	return llm.ParseTextToolCalls(content, validTools, llm.DefaultToolCallTextProfile())
}

// Ping checks if Ollama is reachable.
func (c *OllamaClient) Ping(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API error %d", resp.StatusCode)
	}

	return nil
}

// ListModelInfos returns available models.
func (c *OllamaClient) ListModelInfos(ctx context.Context) ([]OllamaModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody := httpkit.ReadErrorBody(resp.Body, 4096)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, errBody)
	}

	var result struct {
		Models []OllamaModelInfo `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return result.Models, nil
}

// ListModels returns available model names.
func (c *OllamaClient) ListModels(ctx context.Context) ([]string, error) {
	models, err := c.ListModelInfos(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(models))
	for i, m := range models {
		names[i] = m.Name
	}
	return names, nil
}
