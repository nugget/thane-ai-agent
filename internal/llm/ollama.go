// Package llm provides LLM client implementations.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message represents a chat message for the LLM.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // For tool responses
}

// OllamaClient is a client for the Ollama API.
type OllamaClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewOllamaClient creates a new Ollama client.
func NewOllamaClient(baseURL string) *OllamaClient {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return &OllamaClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute, // Large models with tools need time
		},
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

// ToolCall represents a tool call from the model.
type ToolCall struct {
	Function struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"` // Ollama returns object, not string
	} `json:"function"`
}

// ChatResponse is the response from Ollama chat API.
type ChatResponse struct {
	Model     string  `json:"model"`
	CreatedAt string  `json:"created_at"`
	Message   Message `json:"message"`
	Done      bool    `json:"done"`

	// Usage stats (when done=true)
	TotalDuration      int64 `json:"total_duration,omitempty"`
	LoadDuration       int64 `json:"load_duration,omitempty"`
	PromptEvalCount    int   `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64 `json:"prompt_eval_duration,omitempty"`
	EvalCount          int   `json:"eval_count,omitempty"`
	EvalDuration       int64 `json:"eval_duration,omitempty"`
}

// Chat sends a chat completion request to Ollama.
func (c *OllamaClient) Chat(ctx context.Context, model string, messages []Message, tools []map[string]any) (*ChatResponse, error) {
	return c.ChatStream(ctx, model, messages, tools, nil)
}

// StreamCallback is called for each streamed token.
type StreamCallback func(token string)

// ChatStream sends a streaming chat request to Ollama.
// If callback is non-nil, tokens are streamed to it.
func (c *OllamaClient) ChatStream(ctx context.Context, model string, messages []Message, tools []map[string]any, callback StreamCallback) (*ChatResponse, error) {
	stream := callback != nil

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
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	if !stream {
		// Non-streaming: single JSON response
		var chatResp ChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		// Try to parse text-based tool calls if no native tool_calls
		if len(chatResp.Message.ToolCalls) == 0 && chatResp.Message.Content != "" {
			if parsed := parseTextToolCalls(chatResp.Message.Content); len(parsed) > 0 {
				chatResp.Message.ToolCalls = parsed
				chatResp.Message.Content = "" // Clear content since it was a tool call
			}
		}
		return &chatResp, nil
	}

	// Streaming: read newline-delimited JSON
	var finalResp ChatResponse
	var contentBuilder strings.Builder
	decoder := json.NewDecoder(resp.Body)

	for {
		var chunk ChatResponse
		if err := decoder.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode stream chunk: %w", err)
		}

		// Accumulate content
		if chunk.Message.Content != "" {
			contentBuilder.WriteString(chunk.Message.Content)
			if callback != nil {
				callback(chunk.Message.Content)
			}
		}

		// Tool calls come in the final message
		if len(chunk.Message.ToolCalls) > 0 {
			finalResp.Message.ToolCalls = chunk.Message.ToolCalls
		}

		// Capture final metadata
		if chunk.Done {
			finalResp = chunk
			finalResp.Message.Content = contentBuilder.String()
			break
		}
	}

	// Try to parse text-based tool calls if no native tool_calls
	if len(finalResp.Message.ToolCalls) == 0 && finalResp.Message.Content != "" {
		if parsed := parseTextToolCalls(finalResp.Message.Content); len(parsed) > 0 {
			finalResp.Message.ToolCalls = parsed
			finalResp.Message.Content = "" // Clear content since it was a tool call
		}
	}

	return &finalResp, nil
}

// parseTextToolCalls attempts to extract tool calls from content text.
// Many models output tool calls as JSON in the content rather than using
// the native tool_calls field. This function handles common formats:
// - Raw JSON object: {"name": "...", "arguments": {...}}
// - JSON array: [{"name": "...", "arguments": {...}}]
// - Tagged: <tool_call>...</tool_call>
func parseTextToolCalls(content string) []ToolCall {
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

	// Try parsing as array of tool calls
	var calls []struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(content), &calls); err == nil && len(calls) > 0 {
		result := make([]ToolCall, len(calls))
		for i, c := range calls {
			result[i].Function.Name = c.Name
			result[i].Function.Arguments = c.Arguments
		}
		return result
	}

	// Try parsing as single tool call object
	var single struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(content), &single); err == nil && single.Name != "" {
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

	return nil
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
