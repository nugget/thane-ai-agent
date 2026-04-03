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
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/httpkit"
)

// LMStudioClient is a client for LM Studio's OpenAI-compatible API.
type LMStudioClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
	watcher    ReadyWatcher
}

// LMStudioModelInfo describes one model from /v1/models.
type LMStudioModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
}

// NewLMStudioClient creates a new LM Studio client.
func NewLMStudioClient(baseURL, apiKey string, logger *slog.Logger) *LMStudioClient {
	if baseURL == "" {
		baseURL = "http://localhost:1234"
	}
	if logger == nil {
		logger = slog.Default()
	}
	t := httpkit.NewTransport()
	t.ResponseHeaderTimeout = 5 * time.Minute

	return &LMStudioClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  strings.TrimSpace(apiKey),
		logger:  logger.With("provider", "lmstudio"),
		httpClient: httpkit.NewClient(
			httpkit.WithTimeout(0),
			httpkit.WithTransport(t),
			httpkit.WithRetry(3, 2*time.Second),
			httpkit.WithLogger(logger),
		),
	}
}

// SetWatcher sets the connection watcher for health status queries.
func (c *LMStudioClient) SetWatcher(w ReadyWatcher) {
	c.watcher = w
}

// IsReady reports whether LM Studio is currently reachable.
func (c *LMStudioClient) IsReady() bool {
	if c.watcher == nil {
		return true
	}
	return c.watcher.IsReady()
}

type lmStudioChatRequest struct {
	Model         string                 `json:"model"`
	Messages      []lmStudioMessage      `json:"messages"`
	Stream        bool                   `json:"stream,omitempty"`
	Tools         []map[string]any       `json:"tools,omitempty"`
	StreamOptions *lmStudioStreamOptions `json:"stream_options,omitempty"`
}

type lmStudioStreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type lmStudioMessage struct {
	Role       string                `json:"role"`
	Content    any                   `json:"content,omitempty"`
	ToolCallID string                `json:"tool_call_id,omitempty"`
	ToolCalls  []lmStudioToolCallReq `json:"tool_calls,omitempty"`
}

type lmStudioContentPart struct {
	Type     string                 `json:"type"`
	Text     string                 `json:"text,omitempty"`
	ImageURL *lmStudioImageURLBlock `json:"image_url,omitempty"`
}

type lmStudioImageURLBlock struct {
	URL string `json:"url"`
}

type lmStudioToolCallReq struct {
	ID       string                    `json:"id,omitempty"`
	Type     string                    `json:"type"`
	Function lmStudioToolFunctionDelta `json:"function"`
}

type lmStudioToolFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type lmStudioChatResponse struct {
	ID      string               `json:"id,omitempty"`
	Object  string               `json:"object,omitempty"`
	Created int64                `json:"created,omitempty"`
	Model   string               `json:"model,omitempty"`
	Choices []lmStudioChatChoice `json:"choices"`
	Usage   *lmStudioUsage       `json:"usage,omitempty"`
}

type lmStudioChatChoice struct {
	Index        int                      `json:"index"`
	Message      *lmStudioMessageResponse `json:"message,omitempty"`
	Delta        *lmStudioChatDelta       `json:"delta,omitempty"`
	FinishReason *string                  `json:"finish_reason,omitempty"`
}

type lmStudioMessageResponse struct {
	Role      string                  `json:"role,omitempty"`
	Content   any                     `json:"content,omitempty"`
	ToolCalls []lmStudioToolCallDelta `json:"tool_calls,omitempty"`
}

type lmStudioChatDelta struct {
	Role      string                  `json:"role,omitempty"`
	Content   string                  `json:"content,omitempty"`
	ToolCalls []lmStudioToolCallDelta `json:"tool_calls,omitempty"`
}

type lmStudioToolCallDelta struct {
	Index    int                       `json:"index,omitempty"`
	ID       string                    `json:"id,omitempty"`
	Type     string                    `json:"type,omitempty"`
	Function lmStudioToolFunctionDelta `json:"function,omitempty"`
}

type lmStudioUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

type lmStudioModelsResponse struct {
	Data []LMStudioModelInfo `json:"data"`
}

type lmStudioToolAccumulator struct {
	ID   string
	Name string
	Args strings.Builder
}

// Chat sends a non-streaming chat completion request to LM Studio.
func (c *LMStudioClient) Chat(ctx context.Context, model string, messages []Message, tools []map[string]any) (*ChatResponse, error) {
	return c.ChatStream(ctx, model, messages, tools, nil)
}

// ChatStream sends a chat request to LM Studio. If callback is non-nil,
// tokens are streamed via OpenAI-compatible SSE.
func (c *LMStudioClient) ChatStream(ctx context.Context, model string, messages []Message, tools []map[string]any, callback StreamCallback) (*ChatResponse, error) {
	stream := callback != nil

	wireMessages, err := toLMStudioMessages(messages)
	if err != nil {
		return nil, fmt.Errorf("encode messages: %w", err)
	}

	req := lmStudioChatRequest{
		Model:    model,
		Messages: wireMessages,
		Stream:   stream,
		Tools:    tools,
	}
	if stream {
		req.StreamOptions = &lmStudioStreamOptions{IncludeUsage: true}
	}

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	c.logger.Debug("preparing request",
		"model", model,
		"messages", len(messages),
		"tools", len(tools),
		"stream", stream,
	)
	c.logger.Log(ctx, LevelTrace, "request payload", "json", string(jsonData))

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.setAuth(httpReq)

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

	validToolNames := extractToolNames(tools)
	if !stream {
		var wire lmStudioChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		result, err := c.chatResponseFromWire(&wire, validToolNames)
		if err != nil {
			return nil, err
		}
		c.logger.Debug("response received",
			"model", result.Model,
			"input_tokens", result.InputTokens,
			"output_tokens", result.OutputTokens,
			"tool_calls", len(result.Message.ToolCalls),
		)
		c.logger.Log(ctx, LevelTrace, "response content", "content", result.Message.Content)
		return result, nil
	}

	return c.handleStreaming(ctx, model, validToolNames, resp.Body, callback)
}

// Ping checks if LM Studio is reachable.
func (c *LMStudioClient) Ping(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/v1/models", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.setAuth(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody := httpkit.ReadErrorBody(resp.Body, 4096)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, errBody)
	}
	return nil
}

// ListModelInfos returns available LM Studio model names.
func (c *LMStudioClient) ListModelInfos(ctx context.Context) ([]LMStudioModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setAuth(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody := httpkit.ReadErrorBody(resp.Body, 4096)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, errBody)
	}

	var result lmStudioModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result.Data, nil
}

func (c *LMStudioClient) setAuth(req *http.Request) {
	if strings.TrimSpace(c.apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

func (c *LMStudioClient) handleStreaming(ctx context.Context, requestedModel string, validToolNames []string, body io.Reader, callback StreamCallback) (*ChatResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	var (
		eventLines     []string
		contentBuilder strings.Builder
		model          = requestedModel
		createdAt      time.Time
		usage          lmStudioUsage
		toolAcc        = make(map[int]*lmStudioToolAccumulator)
		done           bool
	)

	processEvent := func(data string) error {
		if data == "" {
			return nil
		}
		if data == "[DONE]" {
			return io.EOF
		}

		var chunk lmStudioChatResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decode stream chunk: %w", err)
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Created > 0 {
			createdAt = time.Unix(chunk.Created, 0).UTC()
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.Delta == nil {
				continue
			}
			if choice.Delta.Content != "" {
				contentBuilder.WriteString(choice.Delta.Content)
				callback(StreamEvent{Kind: KindToken, Token: choice.Delta.Content})
			}
			for _, tc := range choice.Delta.ToolCalls {
				acc := toolAcc[tc.Index]
				if acc == nil {
					acc = &lmStudioToolAccumulator{}
					toolAcc[tc.Index] = acc
				}
				if tc.ID != "" {
					acc.ID = tc.ID
				}
				if tc.Function.Name != "" {
					acc.Name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					acc.Args.WriteString(tc.Function.Arguments)
				}
			}
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if len(eventLines) == 0 {
				continue
			}
			err := processEvent(strings.Join(eventLines, "\n"))
			eventLines = eventLines[:0]
			if err == io.EOF {
				done = true
				break
			}
			if err != nil {
				return nil, err
			}
		case strings.HasPrefix(line, "data:"):
			eventLines = append(eventLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if done {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}
	if len(eventLines) > 0 {
		if err := processEvent(strings.Join(eventLines, "\n")); err != nil && err != io.EOF {
			return nil, err
		}
	}

	toolCalls, err := decodeLMStudioToolCalls(toolAcc)
	if err != nil {
		return nil, err
	}

	result := &ChatResponse{
		Model:         model,
		CreatedAt:     createdAt,
		Done:          true,
		InputTokens:   usage.PromptTokens,
		OutputTokens:  usage.CompletionTokens,
		TotalDuration: 0,
	}
	result.Message.Content = contentBuilder.String()
	result.Message.ToolCalls = toolCalls
	applyTextToolFallback(result, validToolNames)

	c.logger.Debug("stream complete",
		"model", result.Model,
		"input_tokens", result.InputTokens,
		"output_tokens", result.OutputTokens,
		"content_len", len(result.Message.Content),
		"tool_calls", len(result.Message.ToolCalls),
	)
	c.logger.Log(ctx, LevelTrace, "stream final content", "content", result.Message.Content)
	return result, nil
}

func (c *LMStudioClient) chatResponseFromWire(wire *lmStudioChatResponse, validToolNames []string) (*ChatResponse, error) {
	if wire == nil {
		return nil, fmt.Errorf("nil response")
	}
	if len(wire.Choices) == 0 || wire.Choices[0].Message == nil {
		return nil, fmt.Errorf("response contained no choices")
	}

	toolCalls, err := decodeLMStudioToolCallsFromSlice(wire.Choices[0].Message.ToolCalls)
	if err != nil {
		return nil, err
	}
	result := &ChatResponse{
		Model:        wire.Model,
		Done:         true,
		InputTokens:  0,
		OutputTokens: 0,
	}
	if wire.Created > 0 {
		result.CreatedAt = time.Unix(wire.Created, 0).UTC()
	}
	if wire.Usage != nil {
		result.InputTokens = wire.Usage.PromptTokens
		result.OutputTokens = wire.Usage.CompletionTokens
	}
	result.Message.Role = wire.Choices[0].Message.Role
	result.Message.Content = lmStudioContentText(wire.Choices[0].Message.Content)
	result.Message.ToolCalls = toolCalls
	applyTextToolFallback(result, validToolNames)
	return result, nil
}

func toLMStudioMessages(msgs []Message) ([]lmStudioMessage, error) {
	out := make([]lmStudioMessage, 0, len(msgs))
	for _, m := range msgs {
		wire := lmStudioMessage{
			Role:       m.Role,
			ToolCallID: m.ToolCallID,
		}
		switch {
		case len(m.Images) > 0:
			parts := make([]lmStudioContentPart, 0, len(m.Images)+1)
			if m.Content != "" {
				parts = append(parts, lmStudioContentPart{Type: "text", Text: m.Content})
			}
			for _, img := range m.Images {
				parts = append(parts, lmStudioContentPart{
					Type: "image_url",
					ImageURL: &lmStudioImageURLBlock{
						URL: "data:" + img.MediaType + ";base64," + img.Data,
					},
				})
			}
			wire.Content = parts
		case m.Content != "":
			wire.Content = m.Content
		}
		if len(m.ToolCalls) > 0 {
			wire.ToolCalls = make([]lmStudioToolCallReq, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				argsJSON, err := json.Marshal(tc.Function.Arguments)
				if err != nil {
					return nil, fmt.Errorf("marshal tool call arguments for %q: %w", tc.Function.Name, err)
				}
				wire.ToolCalls = append(wire.ToolCalls, lmStudioToolCallReq{
					ID:   tc.ID,
					Type: "function",
					Function: lmStudioToolFunctionDelta{
						Name:      tc.Function.Name,
						Arguments: string(argsJSON),
					},
				})
			}
		}
		out = append(out, wire)
	}
	return out, nil
}

func decodeLMStudioToolCalls(accs map[int]*lmStudioToolAccumulator) ([]ToolCall, error) {
	if len(accs) == 0 {
		return nil, nil
	}
	indexes := make([]int, 0, len(accs))
	for idx := range accs {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)

	out := make([]ToolCall, 0, len(indexes))
	for _, idx := range indexes {
		acc := accs[idx]
		if acc == nil || acc.Name == "" {
			continue
		}
		args, err := parseLMStudioToolArguments(acc.Name, acc.Args.String())
		if err != nil {
			return nil, err
		}
		call := ToolCall{ID: acc.ID}
		call.Function.Name = acc.Name
		call.Function.Arguments = args
		out = append(out, call)
	}
	return out, nil
}

func decodeLMStudioToolCallsFromSlice(in []lmStudioToolCallDelta) ([]ToolCall, error) {
	if len(in) == 0 {
		return nil, nil
	}
	accs := make(map[int]*lmStudioToolAccumulator, len(in))
	for i, tc := range in {
		idx := tc.Index
		if idx == 0 && tc.ID == "" && tc.Function.Name == "" && tc.Function.Arguments == "" && len(in) == 1 {
			idx = i
		}
		acc := accs[idx]
		if acc == nil {
			acc = &lmStudioToolAccumulator{}
			accs[idx] = acc
		}
		acc.ID = tc.ID
		acc.Name = tc.Function.Name
		acc.Args.WriteString(tc.Function.Arguments)
	}
	return decodeLMStudioToolCalls(accs)
}

func parseLMStudioToolArguments(name, raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("decode tool arguments for %q: %w", name, err)
	}
	return args, nil
}

func lmStudioContentText(v any) string {
	switch content := v.(type) {
	case nil:
		return ""
	case string:
		return content
	case []any:
		var b strings.Builder
		for _, part := range content {
			partMap, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if kind, _ := partMap["type"].(string); kind == "text" {
				if text, _ := partMap["text"].(string); text != "" {
					b.WriteString(text)
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

func applyTextToolFallback(resp *ChatResponse, validToolNames []string) {
	if resp == nil || len(resp.Message.ToolCalls) > 0 || resp.Message.Content == "" {
		return
	}
	if parsed := parseTextToolCalls(resp.Message.Content, validToolNames); len(parsed) > 0 {
		resp.Message.ToolCalls = parsed
		resp.Message.Content = ""
		return
	}
	if looksLikeHallucinatedToolCall(resp.Message.Content) {
		resp.Message.Content = ""
		return
	}
	resp.Message.Content = stripTrailingToolCallJSON(resp.Message.Content, validToolNames)
}
