// Package llm provides LLM client implementations.
package llm

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

	"github.com/nugget/thane-ai-agent/internal/httpkit"
)

// OllamaClient is a client for the Ollama API.
type OllamaClient struct {
	baseURL    string
	httpClient *http.Client
	logger     *slog.Logger
	watcher    readyChecker // set via SetWatcher for health status
}

// readyChecker is satisfied by connwatch.Watcher. Defined here to avoid
// a direct import cycle between llm and connwatch.
type readyChecker interface {
	IsReady() bool
}

// SetWatcher sets the connection watcher for health status queries.
func (c *OllamaClient) SetWatcher(w readyChecker) {
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

// ChatRequest is the request format for Ollama chat API.
type ChatRequest struct {
	Model    string           `json:"model"`
	Messages []Message        `json:"messages"`
	Stream   bool             `json:"stream"`
	Tools    []map[string]any `json:"tools,omitempty"`
	Options  *Options         `json:"options,omitempty"`
}

// Options are model parameters.
type Options struct {
	Temperature float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

// ollamaWireResponse is the raw JSON response from Ollama's /api/chat endpoint.
// This is a deserialization target only — convert to ChatResponse for internal use.
type ollamaWireResponse struct {
	Model              string  `json:"model"`
	CreatedAt          string  `json:"created_at"`
	Message            Message `json:"message"`
	Done               bool    `json:"done"`
	TotalDuration      int64   `json:"total_duration,omitempty"`
	LoadDuration       int64   `json:"load_duration,omitempty"`
	PromptEvalCount    int     `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64   `json:"prompt_eval_duration,omitempty"`
	EvalCount          int     `json:"eval_count,omitempty"`
	EvalDuration       int64   `json:"eval_duration,omitempty"`
}

// toChatResponse converts an Ollama wire response to the internal ChatResponse type.
func (w *ollamaWireResponse) toChatResponse() *ChatResponse {
	createdAt, _ := time.Parse(time.RFC3339Nano, w.CreatedAt)
	return &ChatResponse{
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
func (c *OllamaClient) Chat(ctx context.Context, model string, messages []Message, tools []map[string]any) (*ChatResponse, error) {
	return c.ChatStream(ctx, model, messages, tools, nil)
}

// ChatStream sends a streaming chat request to Ollama.
// If callback is non-nil, tokens are streamed to it.
func (c *OllamaClient) ChatStream(ctx context.Context, model string, messages []Message, tools []map[string]any, callback StreamCallback) (*ChatResponse, error) {
	stream := callback != nil

	c.logger.Debug("preparing request",
		"model", model,
		"messages", len(messages),
		"tools", len(tools),
		"stream", stream,
	)

	req := ChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   stream,
		Tools:    tools,
	}

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	c.logger.Log(ctx, LevelTrace, "request payload", "json", string(jsonData))

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
		c.logger.Log(ctx, LevelTrace, "response content", "content", chatResp.Message.Content)

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
	var finalResp *ChatResponse
	var toolCalls []ToolCall
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
						callback(StreamEvent{Kind: KindToken, Token: accumulated})
					} else {
						callback(StreamEvent{Kind: KindToken, Token: wire.Message.Content})
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
		finalResp = &ChatResponse{Model: model, Done: true}
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
	c.logger.Log(ctx, LevelTrace, "stream final content", "content", finalResp.Message.Content)

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
	if len(tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if fn, ok := tool["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

// looksLikeToolCall checks if accumulated stream content might be a text-based
// tool call. Used to buffer streaming output until we can determine whether
// the model is emitting a tool call as text or actual prose.
// looksLikeHallucinatedToolCall checks if content is JSON with "name" and "arguments"
// stripTrailingToolCallJSON removes JSON tool call objects appended to the end
// of prose content. Returns the cleaned prose (or original if no trailing JSON found).
func stripTrailingToolCallJSON(content string, validTools []string) string {
	lastBrace := strings.LastIndex(content, "{")
	if lastBrace <= 0 {
		return content
	}
	jsonPart := strings.TrimSpace(content[lastBrace:])
	var obj struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(jsonPart), &obj); err != nil || obj.Name == "" {
		return content
	}
	// It's a tool call shape — strip it regardless of whether the name is valid
	cleaned := strings.TrimSpace(content[:lastBrace])
	if cleaned == "" {
		return content // Don't strip if there's no prose left
	}
	return cleaned
}

// looksLikeHallucinatedToolCall checks if content is JSON with "name" and "arguments"
// fields — the shape of a tool call — but wasn't matched by parseTextToolCalls
// (meaning the tool name is invalid). This is a hallucinated tool call that should
// be suppressed rather than shown to the user.
func looksLikeHallucinatedToolCall(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed[0] != '{' {
		return false
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return false
	}
	_, hasName := obj["name"]
	_, hasArgs := obj["arguments"]
	return hasName && hasArgs
}

func looksLikeToolCall(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	// JSON object that might contain "name" — common tool call format
	if trimmed[0] == '{' {
		return true
	}
	// <tool_call> tag format
	if strings.HasPrefix(trimmed, "<tool_call>") || strings.HasPrefix(trimmed, "<tool") {
		return true
	}
	return false
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
func parseTextToolCalls(content string, validTools []string) []ToolCall {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	// Try to extract from <tool_call> tags
	if strings.Contains(content, "<tool_call>") {
		start := strings.Index(content, "<tool_call>")
		end := strings.Index(content, "</tool_call>")
		if start != -1 && end > start {
			content = strings.TrimSpace(content[start+len("<tool_call>") : end])
		} else if start != -1 {
			// No closing tag, take rest of content
			content = strings.TrimSpace(content[start+len("<tool_call>"):])
		}
	}

	var result []ToolCall

	// Try parsing as array of tool calls
	var calls []struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(content), &calls); err == nil && len(calls) > 0 {
		for _, c := range calls {
			if c.Name == "" {
				continue
			}
			if !isValidTool(c.Name, validTools) {
				continue
			}
			result = append(result, ToolCall{
				Function: struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				}{
					Name:      c.Name,
					Arguments: c.Arguments,
				},
			})
		}
		return result
	}

	// Try parsing as single tool call object
	var single struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(content), &single); err == nil && single.Name != "" {
		if isValidTool(single.Name, validTools) {
			return []ToolCall{{
				Function: struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				}{
					Name:      single.Name,
					Arguments: single.Arguments,
				},
			}}
		}
	}

	// Try parsing concatenated JSON objects: {"name":"a","arguments":{}}{"name":"b","arguments":{}}
	// Common with qwen and other models that emit multiple tool calls as adjacent JSON blobs.
	if strings.Count(content, `"name"`) > 1 && strings.Contains(content, "}{") {
		dec := json.NewDecoder(strings.NewReader(content))
		for dec.More() {
			var tc struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := dec.Decode(&tc); err != nil {
				break
			}
			if tc.Name != "" && isValidTool(tc.Name, validTools) {
				result = append(result, ToolCall{
					Function: struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					}{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				})
			}
		}
		if len(result) > 0 {
			return result
		}
	}

	// Try parsing "tool_name {json_args}" format (common with some models)
	// e.g., "find_entity {"description": "access point LED", "area": "office"}"
	for _, toolName := range validTools {
		prefix := toolName + " "
		if strings.HasPrefix(content, prefix) {
			argsJSON := strings.TrimPrefix(content, prefix)
			// Try to find where the JSON ends (handle trailing text)
			argsJSON = strings.TrimSpace(argsJSON)

			// Find the matching closing brace
			if strings.HasPrefix(argsJSON, "{") {
				depth := 0
				endIdx := -1
				for i, c := range argsJSON {
					if c == '{' {
						depth++
					} else if c == '}' {
						depth--
						if depth == 0 {
							endIdx = i + 1
							break
						}
					}
				}
				if endIdx > 0 {
					argsJSON = argsJSON[:endIdx]
				}
			}

			var args map[string]any
			if err := json.Unmarshal([]byte(argsJSON), &args); err == nil {
				return []ToolCall{{
					Function: struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					}{
						Name:      toolName,
						Arguments: args,
					},
				}}
			}
		}
	}

	return nil
}

// isValidTool checks if a tool name is in the valid tools list.
// Returns true if validTools is nil or empty (no validation).
func isValidTool(name string, validTools []string) bool {
	if len(validTools) == 0 {
		return true
	}
	for _, v := range validTools {
		if v == name {
			return true
		}
	}
	return false
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

// ListModels returns available models.
func (c *OllamaClient) ListModels(ctx context.Context) ([]string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	names := make([]string, len(result.Models))
	for i, m := range result.Models {
		names[i] = m.Name
	}
	return names, nil
}
