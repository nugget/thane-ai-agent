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

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/httpkit"
)

// OpenAICompatClient speaks the OpenAI chat-completions protocol to any
// server that implements it — vLLM, SGLang, llama.cpp's llama-server,
// LM Studio, NVIDIA NIM, and Ollama's own /v1 surface all qualify.
//
// It exists because the protocol, not the vendor, is the real contract.
// The provider-specific Ollama client this supersedes had to relearn
// streaming, tool-call accumulation, usage accounting, and error framing
// on its own, and got the last two wrong: it sent no usage request and
// had no error field on its chunk type, so an upstream failure mid-stream
// decoded as an empty chunk and a dead stream became a Done:true success
// carrying zero tokens. Everything here is shared with the LM Studio
// client, which has been exercising this path in production.
//
// What stays out: anything a specific server adds beyond the protocol.
// LM Studio's model load/unload lives on [LMStudioClient], which embeds
// this type rather than reimplementing it.
type OpenAICompatClient struct {
	baseURL string
	apiKey  string
	// provider labels logs and metrics with the server flavor behind
	// this endpoint. The wire protocol is identical either way; this is
	// for operators reading logs, not for behavior.
	provider string
	// idleTTLSeconds populates the LM Studio `ttl` field, which asks the
	// server to release the model after an idle period. Omitted from the
	// request when zero, which is what every other OpenAI-compatible
	// server wants — they have no such field and reject unknown ones
	// inconsistently.
	idleTTLSeconds int
	httpClient     *http.Client
	logger         *slog.Logger
	watcher        llm.ReadyWatcher
}

// NewOpenAICompatClient creates a client for an OpenAI-compatible
// endpoint. provider is a label for logs (e.g. "vllm", "ollama"); it does
// not change the wire protocol. Pass idleTTLSeconds only for servers that
// honor the LM Studio `ttl` extension.
func NewOpenAICompatClient(baseURL, apiKey, provider string, logger *slog.Logger, idleTTLSeconds int) *OpenAICompatClient {
	if baseURL == "" {
		baseURL = "http://localhost:1234"
	}
	if logger == nil {
		logger = slog.Default()
	}
	if strings.TrimSpace(provider) == "" {
		provider = "openai_compat"
	}
	if idleTTLSeconds < 0 {
		idleTTLSeconds = 0
	}
	// No total client timeout: a long generation is not a hung request,
	// and a deadline spanning the whole stream kills healthy work at the
	// worst possible moment. Liveness is enforced by the response-header
	// timeout instead, which bounds how long the server may go silent
	// before producing anything.
	t := httpkit.NewTransport()
	t.ResponseHeaderTimeout = 5 * time.Minute

	return &OpenAICompatClient{
		baseURL:        normalizeOpenAICompatBaseURL(baseURL),
		apiKey:         strings.TrimSpace(apiKey),
		provider:       strings.TrimSpace(provider),
		idleTTLSeconds: idleTTLSeconds,
		logger:         logger.With("provider", strings.TrimSpace(provider)),
		httpClient: httpkit.NewClient(
			httpkit.WithTimeout(0),
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
func (c *OpenAICompatClient) SetLogger(logger *slog.Logger) {
	if c == nil || logger == nil {
		return
	}
	c.logger = logger.With("provider", c.provider)
}

// Logger returns the request-level logger. Exposed for tests and
// late-bind verification — production callers should not depend on it.
func (c *OpenAICompatClient) Logger() *slog.Logger {
	if c == nil {
		return nil
	}
	return c.logger
}

// AttachWatcher sets the connection watcher for health status queries.
func (c *OpenAICompatClient) AttachWatcher(w llm.ReadyWatcher) {
	c.watcher = w
}

// IsReady reports whether the endpoint is currently reachable.
func (c *OpenAICompatClient) IsReady() bool {
	if c.watcher == nil {
		return true
	}
	return c.watcher.IsReady()
}

// Chat sends a non-streaming chat completion request.
func (c *OpenAICompatClient) Chat(ctx context.Context, model string, messages []llm.Message, tools []map[string]any) (*llm.ChatResponse, error) {
	return c.ChatStream(ctx, model, messages, tools, nil)
}

// ChatStream sends a chat request. If callback is non-nil,
// tokens are streamed via OpenAI-compatible SSE.
func (c *OpenAICompatClient) ChatStream(ctx context.Context, model string, messages []llm.Message, tools []map[string]any, callback llm.StreamCallback) (*llm.ChatResponse, error) {
	stream := callback != nil

	wireMessages, err := toOpenAICompatMessages(messages)
	if err != nil {
		return nil, fmt.Errorf("encode messages: %w", err)
	}

	req := openAICompatChatRequest{
		Model:    model,
		Messages: wireMessages,
		Stream:   stream,
		Tools:    tools,
		TTL:      c.idleTTLSeconds,
		// No ceiling of our own to defend: these servers enforce what
		// their launch configured, so the only limit worth sending is
		// the caller's remaining budget, and 0 leaves the field off.
		MaxTokens: llm.MaxOutputTokensFromContext(ctx),
	}
	if stream {
		req.StreamOptions = &openAICompatStreamOptions{IncludeUsage: true}
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
		var wire openAICompatChatResponse
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

func (c *OpenAICompatClient) setAuth(req *http.Request) {
	if strings.TrimSpace(c.apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

func (c *OpenAICompatClient) handleStreaming(ctx context.Context, requestedModel string, validToolNames []string, body io.Reader, callback llm.StreamCallback) (*llm.ChatResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	var (
		eventLines     []string
		contentBuilder strings.Builder
		model          = requestedModel
		role           = "assistant"
		createdAt      time.Time
		usage          openAICompatUsage
		toolAcc        = make(map[int]*openAICompatToolAccumulator)
		done           bool
		chunks         int
		finishReason   string
		upstreamID     string
	)

	processEvent := func(data string) error {
		if data == "" {
			return nil
		}
		if data == "[DONE]" {
			return io.EOF
		}
		// An error frame decodes cleanly into openAICompatChatResponse — every
		// field it carries is unknown to that type — so it would otherwise
		// pass through as a chunk contributing nothing, turning an upstream
		// failure into a silently empty completion. Check for it first.
		if errText := openAICompatStreamErrorText(data); errText != "" {
			return fmt.Errorf("stream error: %s", errText)
		}

		var chunk openAICompatChatResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decode stream chunk: %w", err)
		}
		chunks++
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.ID != "" {
			upstreamID = chunk.ID
		}
		if chunk.Created > 0 {
			createdAt = time.Unix(chunk.Created, 0).UTC()
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		for _, choice := range chunk.Choices {
			// The terminating frame carries finish_reason with an empty
			// delta, so read it before the delta guard skips the choice.
			// This is the provider's own termination signal — "length",
			// "tool_calls", "stop", and the pause_turn family — which
			// the iteration layer records and operators read to tell a
			// truncated answer from a complete one.
			if choice.FinishReason != nil && strings.TrimSpace(*choice.FinishReason) != "" {
				finishReason = strings.TrimSpace(*choice.FinishReason)
			}
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
					acc = &openAICompatToolAccumulator{}
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

	toolCalls, err := decodeOpenAICompatToolCalls(toolAcc)
	if err != nil {
		return nil, err
	}

	// Frame count is not evidence of a completion. A role-only opening
	// frame or a usage-only trailer both increment it, so a stream that
	// carried nothing but scaffolding and then closed cleanly would pass
	// a chunks>0 check and return Done:true with no content, no tool
	// calls, and zero tokens — the fabricated success this client exists
	// to refuse. Judge the same thing the non-streaming path judges:
	// whether an assistant actually said or did anything.
	if strings.TrimSpace(contentBuilder.String()) == "" && len(toolCalls) == 0 {
		return nil, fmt.Errorf("%s returned an empty stream for model %q (frames=%d)", c.provider, requestedModel, chunks)
	}

	// Content alone does not mean the answer finished. A proxy or runner
	// that dies mid-generation closes the connection cleanly, so the
	// scanner sees an ordinary EOF and everything received so far looks
	// like a completion — a truncated answer presented as a whole one,
	// which is the same lie as a fabricated success wearing better
	// clothes. Require the server to have said it was done: either the
	// [DONE] sentinel or a finish_reason on some choice. Servers vary in
	// which they send, so either suffices; neither is silence.
	if !done && finishReason == "" {
		return nil, fmt.Errorf("%s ended the stream for model %q without a terminal marker after %d frames (truncated response)", c.provider, requestedModel, chunks)
	}

	result := &llm.ChatResponse{
		Model:             model,
		CreatedAt:         createdAt,
		Done:              true,
		StopReason:        finishReason,
		UpstreamRequestID: upstreamID,
		InputTokens:       usage.PromptTokens,
		OutputTokens:      usage.CompletionTokens,
		TotalDuration:     0,
	}
	result.Message.Role = normalizeOpenAICompatMessageRole(role)
	result.Message.Content = contentBuilder.String()
	result.Message.ToolCalls = toolCalls
	if err := applyTextToolFallback(result, validToolNames); err != nil {
		return nil, err
	}

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

func (c *OpenAICompatClient) chatResponseFromWire(wire *openAICompatChatResponse, validToolNames []string) (*llm.ChatResponse, error) {
	if wire == nil {
		return nil, fmt.Errorf("nil response")
	}
	if len(wire.Choices) == 0 || wire.Choices[0].Message == nil {
		return nil, fmt.Errorf("response contained no choices")
	}

	toolCalls, err := decodeOpenAICompatToolCallsFromSlice(wire.Choices[0].Message.ToolCalls)
	if err != nil {
		return nil, err
	}
	result := &llm.ChatResponse{
		Model:        wire.Model,
		Done:         true,
		InputTokens:  0,
		OutputTokens: 0,
	}
	if fr := wire.Choices[0].FinishReason; fr != nil {
		result.StopReason = strings.TrimSpace(*fr)
	}
	result.UpstreamRequestID = wire.ID
	if wire.Created > 0 {
		result.CreatedAt = time.Unix(wire.Created, 0).UTC()
	}
	if wire.Usage != nil {
		result.InputTokens = wire.Usage.PromptTokens
		result.OutputTokens = wire.Usage.CompletionTokens
	}
	result.Message.Role = normalizeOpenAICompatMessageRole(wire.Choices[0].Message.Role)
	result.Message.Content = openAICompatContentText(wire.Choices[0].Message.Content)
	result.Message.ToolCalls = toolCalls
	if err := applyTextToolFallback(result, validToolNames); err != nil {
		return nil, err
	}
	if strings.TrimSpace(result.Message.Content) == "" && len(result.Message.ToolCalls) == 0 {
		return nil, fmt.Errorf("%s returned an empty assistant completion for model %q", c.provider, wire.Model)
	}
	return result, nil
}

// normalizeOpenAICompatBaseURL trims the trailing slash and the
// conventional /v1 suffix. Every documented base URL for these servers
// is written one way or the other — vLLM's own examples end in /v1,
// LM Studio's do not — and this client appends /v1 itself, so accepting
// only the bare form turns a reasonable config into requests for
// /v1/v1/chat/completions and a 404 that names nothing useful.
func normalizeOpenAICompatBaseURL(raw string) string {
	u := strings.TrimRight(strings.TrimSpace(raw), "/")
	return strings.TrimSuffix(u, "/v1")
}
