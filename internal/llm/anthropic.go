package llm

import (
	"bufio"
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

const (
	anthropicAPIURL     = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion = "2023-06-01"
)

// AnthropicClient is a client for the Anthropic Messages API.
type AnthropicClient struct {
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
}

// NewAnthropicClient creates a new Anthropic client.
func NewAnthropicClient(apiKey string, logger *slog.Logger) *AnthropicClient {
	if logger == nil {
		logger = slog.Default()
	}
	// LLM responses can take significant time before sending headers
	// (thinking, long prompts). Use a custom transport with a generous
	// response header timeout. Streaming and non-streaming (compaction)
	// requests both benefit.
	t := httpkit.NewTransport()
	t.ResponseHeaderTimeout = 120 * time.Second

	return &AnthropicClient{
		apiKey: apiKey,
		logger: logger.With("provider", "anthropic"),
		httpClient: httpkit.NewClient(
			// No global timeout — streaming responses can be long-lived.
			// Rely on ctx deadlines/cancellation for timeout control.
			httpkit.WithTimeout(0),
			httpkit.WithTransport(t),
		),
	}
}

// Anthropic request/response types

type anthropicRequest struct {
	Model     string             `json:"model"`
	Messages  []anthropicMessage `json:"messages"`
	System    string             `json:"system,omitempty"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream,omitempty"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []anthropicContent
}

type anthropicContent struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"` // for tool_result
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema"`
}

type anthropicResponse struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Content      []anthropicContent `json:"content"`
	Model        string             `json:"model"`
	StopReason   string             `json:"stop_reason"`
	StopSequence *string            `json:"stop_sequence"`
	Usage        anthropicUsage     `json:"usage"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// SSE event types for streaming
type anthropicStreamEvent struct {
	Type         string             `json:"type"`
	Index        int                `json:"index,omitempty"`
	ContentBlock *anthropicContent  `json:"content_block,omitempty"`
	Delta        *anthropicDelta    `json:"delta,omitempty"`
	Message      *anthropicResponse `json:"message,omitempty"`
	Usage        *anthropicUsage    `json:"usage,omitempty"`
}

type anthropicDelta struct {
	Type         string `json:"type,omitempty"`
	Text         string `json:"text,omitempty"`
	PartialJSON  string `json:"partial_json,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	StopSequence string `json:"stop_sequence,omitempty"`
}

// Chat sends a non-streaming chat completion request.
func (c *AnthropicClient) Chat(ctx context.Context, model string, messages []Message, tools []map[string]any) (*ChatResponse, error) {
	return c.ChatStream(ctx, model, messages, tools, nil)
}

// ChatStream sends a chat request, optionally streaming tokens via callback.
func (c *AnthropicClient) ChatStream(ctx context.Context, model string, messages []Message, tools []map[string]any, callback StreamCallback) (*ChatResponse, error) {
	stream := callback != nil

	// Convert messages and extract system prompt
	anthropicMsgs, systemPrompt := convertToAnthropic(messages)
	anthropicTools := convertToolsToAnthropic(tools)

	c.logger.Debug("preparing request",
		"model", model,
		"messages", len(anthropicMsgs),
		"tools", len(anthropicTools),
		"stream", stream,
		"system_len", len(systemPrompt),
	)

	req := anthropicRequest{
		Model:     model,
		Messages:  anthropicMsgs,
		System:    systemPrompt,
		MaxTokens: 4096,
		Stream:    stream,
		Tools:     anthropicTools,
	}

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	c.logger.Log(ctx, LevelTrace, "request payload", "json", string(jsonData))

	httpReq, err := http.NewRequestWithContext(ctx, "POST", anthropicAPIURL, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody := httpkit.ReadErrorBody(resp.Body, 4096)
		c.logger.Error("API error", "status", resp.StatusCode, "body", errBody)
		return nil, fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, errBody)
	}

	if !stream {
		return c.handleNonStreaming(ctx, resp.Body)
	}
	return c.handleStreaming(ctx, resp.Body, callback)
}

// Ping checks if the Anthropic API is reachable.
func (c *AnthropicClient) Ping(ctx context.Context) error {
	// Anthropic doesn't have a dedicated health endpoint.
	// We'll send a minimal request to verify the API key works.
	req := anthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		Messages:  []anthropicMessage{{Role: "user", Content: "ping"}},
		MaxTokens: 1,
	}

	jsonData, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", anthropicAPIURL, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("invalid API key")
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status from Anthropic API: %d", httpResp.StatusCode)
	}
	return nil
}

func (c *AnthropicClient) handleNonStreaming(ctx context.Context, body io.Reader) (*ChatResponse, error) {
	var resp anthropicResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	result := convertFromAnthropic(&resp)

	c.logger.Debug("response received",
		"model", result.Model,
		"input_tokens", result.InputTokens,
		"output_tokens", result.OutputTokens,
		"tool_calls", len(result.Message.ToolCalls),
	)
	c.logger.Log(ctx, LevelTrace, "response content", "content", result.Message.Content)

	return result, nil
}

func (c *AnthropicClient) handleStreaming(ctx context.Context, body io.Reader, callback StreamCallback) (*ChatResponse, error) {
	scanner := bufio.NewScanner(body)
	// Increase scanner buffer for large responses
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		contentBuilder strings.Builder
		toolCalls      []ToolCall
		currentTool    *anthropicContent // Track in-progress tool_use block
		toolJSONBuf    strings.Builder
		stopReason     string
		usage          anthropicUsage
		model          string
	)

	for scanner.Scan() {
		line := scanner.Text()

		// SSE format: "event: <type>" followed by "data: <json>"
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "[DONE]" {
			break
		}

		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue // Skip malformed events
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				model = event.Message.Model
				usage = event.Message.Usage
			}

		case "content_block_start":
			if event.ContentBlock != nil {
				switch event.ContentBlock.Type {
				case "tool_use":
					currentTool = event.ContentBlock
					toolJSONBuf.Reset()
				}
			}

		case "content_block_delta":
			if event.Delta != nil {
				switch event.Delta.Type {
				case "text_delta":
					contentBuilder.WriteString(event.Delta.Text)
					if callback != nil {
						callback(StreamEvent{Kind: KindToken, Token: event.Delta.Text})
					}
				case "input_json_delta":
					toolJSONBuf.WriteString(event.Delta.PartialJSON)
				}
			}

		case "content_block_stop":
			if currentTool != nil {
				// Parse accumulated tool arguments
				var args map[string]any
				if toolJSONBuf.Len() > 0 {
					if err := json.Unmarshal([]byte(toolJSONBuf.String()), &args); err != nil {
						args = map[string]any{"_raw": toolJSONBuf.String()}
					}
				}
				toolCalls = append(toolCalls, ToolCall{
					ID: currentTool.ID,
					Function: struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					}{
						Name:      currentTool.Name,
						Arguments: args,
					},
				})
				currentTool = nil
			}

		case "message_delta":
			if event.Delta != nil {
				stopReason = event.Delta.StopReason
			}
			if event.Usage != nil {
				usage.OutputTokens = event.Usage.OutputTokens
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}

	resp := &ChatResponse{
		Model: model,
		Message: Message{
			Role:      "assistant",
			Content:   contentBuilder.String(),
			ToolCalls: toolCalls,
		},
		Done:         true,
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
	}

	// stopReason available for future use (end_turn, tool_use, max_tokens, stop_sequence)
	_ = stopReason

	c.logger.Debug("stream complete",
		"model", resp.Model,
		"input_tokens", resp.InputTokens,
		"output_tokens", resp.OutputTokens,
		"content_len", len(resp.Message.Content),
		"tool_calls", len(resp.Message.ToolCalls),
	)
	c.logger.Log(ctx, LevelTrace, "stream final content", "content", resp.Message.Content)

	return resp, nil
}

// convertToAnthropic converts internal messages to Anthropic format.
// Extracts system messages into a separate system prompt.
func convertToAnthropic(messages []Message) ([]anthropicMessage, string) {
	var systemParts []string
	var result []anthropicMessage

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			systemParts = append(systemParts, msg.Content)

		case "assistant":
			if len(msg.ToolCalls) > 0 {
				// Assistant message with tool calls → content blocks
				var blocks []anthropicContent
				if msg.Content != "" {
					blocks = append(blocks, anthropicContent{
						Type: "text",
						Text: msg.Content,
					})
				}
				for i, tc := range msg.ToolCalls {
					args := tc.Function.Arguments
					if args == nil {
						args = map[string]any{}
					}
					id := tc.ID
					if id == "" {
						id = fmt.Sprintf("toolu_%s_%d", tc.Function.Name, i)
					}
					blocks = append(blocks, anthropicContent{
						Type:  "tool_use",
						ID:    id,
						Name:  tc.Function.Name,
						Input: args,
					})
				}
				result = append(result, anthropicMessage{
					Role:    "assistant",
					Content: blocks,
				})
			} else {
				result = append(result, anthropicMessage{
					Role:    "assistant",
					Content: msg.Content,
				})
			}

		case "tool":
			// Tool responses → tool_result content blocks
			result = append(result, anthropicMessage{
				Role: "user",
				Content: []anthropicContent{{
					Type:      "tool_result",
					ToolUseID: msg.ToolCallID,
					Content:   msg.Content,
				}},
			})

		case "user":
			result = append(result, anthropicMessage{
				Role:    "user",
				Content: msg.Content,
			})
		}
	}

	system := strings.Join(systemParts, "\n\n")
	return result, system
}

// convertToolsToAnthropic converts OpenAI-format tool definitions to Anthropic format.
func convertToolsToAnthropic(tools []map[string]any) []anthropicTool {
	if len(tools) == 0 {
		return nil
	}

	var result []anthropicTool
	for _, tool := range tools {
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}

		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		params := fn["parameters"]

		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}

		result = append(result, anthropicTool{
			Name:        name,
			Description: desc,
			InputSchema: params,
		})
	}
	return result
}

// convertFromAnthropic converts an Anthropic response to our internal format.
func convertFromAnthropic(resp *anthropicResponse) *ChatResponse {
	var content string
	var toolCalls []ToolCall

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			content += block.Text
		case "tool_use":
			args, ok := block.Input.(map[string]any)
			if !ok {
				args = map[string]any{}
			}
			toolCalls = append(toolCalls, ToolCall{
				ID: block.ID,
				Function: struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				}{
					Name:      block.Name,
					Arguments: args,
				},
			})
		}
	}

	return &ChatResponse{
		Model: resp.Model,
		Message: Message{
			Role:      resp.Role,
			Content:   content,
			ToolCalls: toolCalls,
		},
		Done:         true,
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
	}
}

// (toolUseID removed — IDs are now carried on ToolCall.ID directly)
