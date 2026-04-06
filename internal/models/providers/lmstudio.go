package providers

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
	"github.com/nugget/thane-ai-agent/internal/llm"
)

// LMStudioClient is a client for LM Studio's OpenAI-compatible API.
type LMStudioClient struct {
	baseURL        string
	apiKey         string
	idleTTLSeconds int
	httpClient     *http.Client
	logger         *slog.Logger
	watcher        llm.ReadyWatcher
}

// NewLMStudioClient creates a new LM Studio client.
func NewLMStudioClient(baseURL, apiKey string, logger *slog.Logger) *LMStudioClient {
	return NewLMStudioClientWithTTL(baseURL, apiKey, logger, 0)
}

// NewLMStudioClientWithTTL creates a new LM Studio client with a
// resource-level idle TTL hint for inference requests.
func NewLMStudioClientWithTTL(baseURL, apiKey string, logger *slog.Logger, idleTTLSeconds int) *LMStudioClient {
	if baseURL == "" {
		baseURL = "http://localhost:1234"
	}
	if logger == nil {
		logger = slog.Default()
	}
	if idleTTLSeconds < 0 {
		idleTTLSeconds = 0
	}
	t := httpkit.NewTransport()
	t.ResponseHeaderTimeout = 5 * time.Minute

	return &LMStudioClient{
		baseURL:        strings.TrimRight(baseURL, "/"),
		apiKey:         strings.TrimSpace(apiKey),
		idleTTLSeconds: idleTTLSeconds,
		logger:         logger.With("provider", "lmstudio"),
		httpClient: httpkit.NewClient(
			httpkit.WithTimeout(0),
			httpkit.WithTransport(t),
			httpkit.WithRetry(3, 2*time.Second),
			httpkit.WithLogger(logger),
		),
	}
}

// AttachWatcher sets the connection watcher for health status queries.
func (c *LMStudioClient) AttachWatcher(w llm.ReadyWatcher) {
	c.watcher = w
}

// IsReady reports whether LM Studio is currently reachable.
func (c *LMStudioClient) IsReady() bool {
	if c.watcher == nil {
		return true
	}
	return c.watcher.IsReady()
}

// Chat sends a non-streaming chat completion request to LM Studio.
func (c *LMStudioClient) Chat(ctx context.Context, model string, messages []llm.Message, tools []map[string]any) (*llm.ChatResponse, error) {
	return c.ChatStream(ctx, model, messages, tools, nil)
}

// ChatStream sends a chat request to LM Studio. If callback is non-nil,
// tokens are streamed via OpenAI-compatible SSE.
func (c *LMStudioClient) ChatStream(ctx context.Context, model string, messages []llm.Message, tools []map[string]any, callback llm.StreamCallback) (*llm.ChatResponse, error) {
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
		TTL:      c.idleTTLSeconds,
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
		"idle_ttl_seconds", c.idleTTLSeconds,
	)
	c.logger.Log(ctx, llm.LevelTrace, "request payload", "json", string(jsonData))

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
		c.logger.Log(ctx, llm.LevelTrace, "response content", "content", result.Message.Content)
		return result, nil
	}

	return c.handleStreaming(ctx, model, validToolNames, resp.Body, callback)
}

func (c *LMStudioClient) setAuth(req *http.Request) {
	if strings.TrimSpace(c.apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

// LoadModel asks LM Studio to load or reload a model with the requested
// inference context length.
func (c *LMStudioClient) LoadModel(ctx context.Context, model string, contextLength int) (*LMStudioLoadResponse, error) {
	reqBody := lmStudioLoadRequest{
		Model:          strings.TrimSpace(model),
		ContextLength:  contextLength,
		EchoLoadConfig: true,
	}
	if reqBody.Model == "" {
		return nil, fmt.Errorf("model is required")
	}
	if reqBody.ContextLength < 0 {
		reqBody.ContextLength = 0
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v1/models/load", bytes.NewReader(jsonData))
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
		c.logger.Error("load model API error", "status", resp.StatusCode, "body", errBody, "model", reqBody.Model, "context_length", reqBody.ContextLength)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, errBody)
	}

	var result LMStudioLoadResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	c.logger.Info("model loaded",
		"model", reqBody.Model,
		"context_length", reqBody.ContextLength,
		"status", result.Status,
		"instance_id", result.InstanceID,
	)
	return &result, nil
}

func (c *LMStudioClient) handleStreaming(ctx context.Context, requestedModel string, validToolNames []string, body io.Reader, callback llm.StreamCallback) (*llm.ChatResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	var (
		eventLines     []string
		contentBuilder strings.Builder
		model          = requestedModel
		role           = "assistant"
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
			if choice.Delta.Role != "" {
				role = choice.Delta.Role
			}
			if choice.Delta.Content != "" {
				contentBuilder.WriteString(choice.Delta.Content)
				callback(llm.StreamEvent{Kind: llm.KindToken, Token: choice.Delta.Content})
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

	result := &llm.ChatResponse{
		Model:         model,
		CreatedAt:     createdAt,
		Done:          true,
		InputTokens:   usage.PromptTokens,
		OutputTokens:  usage.CompletionTokens,
		TotalDuration: 0,
	}
	result.Message.Role = normalizeLMStudioMessageRole(role)
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
	c.logger.Log(ctx, llm.LevelTrace, "stream final content", "content", result.Message.Content)
	return result, nil
}

func (c *LMStudioClient) chatResponseFromWire(wire *lmStudioChatResponse, validToolNames []string) (*llm.ChatResponse, error) {
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
	result := &llm.ChatResponse{
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
	result.Message.Role = normalizeLMStudioMessageRole(wire.Choices[0].Message.Role)
	result.Message.Content = lmStudioContentText(wire.Choices[0].Message.Content)
	result.Message.ToolCalls = toolCalls
	applyTextToolFallback(result, validToolNames)
	if strings.TrimSpace(result.Message.Content) == "" && len(result.Message.ToolCalls) == 0 {
		return nil, fmt.Errorf("LM Studio returned an empty assistant completion for model %q", wire.Model)
	}
	return result, nil
}
