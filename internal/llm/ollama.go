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
}

// NewOllamaClient creates a new Ollama client.
func NewOllamaClient(baseURL string, logger *slog.Logger) *OllamaClient {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &OllamaClient{
		baseURL: baseURL,
		logger:  logger.With("provider", "ollama"),
		httpClient: httpkit.NewClient(
			httpkit.WithTimeout(5 * time.Minute), // Large models with tools need time
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
			}
		}
		return chatResp, nil
	}

	// Streaming: read newline-delimited JSON
	var finalResp *ChatResponse
	var toolCalls []ToolCall
	var contentBuilder strings.Builder
	decoder := json.NewDecoder(resp.Body)

	for {
		var wire ollamaWireResponse
		if err := decoder.Decode(&wire); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode stream chunk: %w", err)
		}

		// Accumulate content
		if wire.Message.Content != "" {
			contentBuilder.WriteString(wire.Message.Content)
			if callback != nil {
				callback(StreamEvent{Kind: KindToken, Token: wire.Message.Content})
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
		}
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
