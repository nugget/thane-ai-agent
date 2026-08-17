package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/httpkit"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
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
	// streamIdleTimeout bounds silence, not duration: how long the
	// server may send nothing before the request is abandoned. Zero
	// disables the guard, which is what the pre-existing behavior was
	// and what a test wanting to hold a stream open asks for.
	streamIdleTimeout time.Duration
	// resource names the configured endpoint this client serves. Kept
	// as a value, not only baked into c.logger, so it can be re-applied
	// onto the request-scoped logger from context.
	resource   string
	httpClient *http.Client
	logger     *slog.Logger
	watcher    llm.ReadyWatcher
}

// NewOpenAICompatClient creates a client for an OpenAI-compatible
// endpoint. provider is a label for logs (e.g. "vllm", "ollama"); it does
// not change the wire protocol. Pass idleTTLSeconds only for servers that
// honor the LM Studio `ttl` extension.
func NewOpenAICompatClient(baseURL, apiKey, provider, resource string, logger *slog.Logger, idleTTLSeconds int) *OpenAICompatClient {
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
		baseURL:           normalizeOpenAICompatBaseURL(baseURL),
		apiKey:            strings.TrimSpace(apiKey),
		provider:          strings.TrimSpace(provider),
		resource:          strings.TrimSpace(resource),
		idleTTLSeconds:    idleTTLSeconds,
		streamIdleTimeout: DefaultStreamIdleTimeout,
		logger:            logger.With("provider", strings.TrimSpace(provider)),
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

	// Built before the first line this call writes, so every one of them
	// carries the same identity — the request and the endpoint together.
	// A logger created after the early lines leaves them orphaned from
	// the turn that produced them, which is the gap this change exists
	// to close.
	log := c.callLogger(ctx).With("model", model, "stream", stream)
	started := time.Now()

	log.Debug("preparing request",
		"messages", len(messages),
		"tools", len(tools),
		"idle_ttl_seconds", c.idleTTLSeconds,
		"max_tokens", req.MaxTokens,
	)
	log.Log(ctx, llm.LevelTrace, "request payload", "json", string(jsonData))

	// The request context must be cancellable before the request is
	// built: the idle guard unblocks a stalled read by cancelling this
	// request, and cancelling any later-derived context would leave the
	// read exactly where it was. The call logger rides along on it, so
	// the retry transport — which only ever sees the request — logs with
	// the same attributes rather than falling back to whatever handler
	// it was constructed with.
	reqCtx, cancelReq := context.WithCancel(logging.WithLogger(ctx, log))
	httpReq, err := http.NewRequestWithContext(reqCtx, "POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(jsonData))
	if err != nil {
		cancelReq()
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.setAuth(httpReq)
	// Claim an identity upstream. A request that dies at the network
	// layer never receives a server-side id, and that is exactly the
	// failure worth correlating; a header the caller set is the only
	// handle that exists on both sides of it.
	if clientRequestID := logging.RequestIDFromContext(ctx); clientRequestID != "" {
		httpReq.Header.Set("X-Client-Request-Id", clientRequestID)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		cancelReq()
		log.Error("request failed",
			"error", err,
			"elapsed_ms", time.Since(started).Milliseconds(),
		)
		return nil, fmt.Errorf("request failed: %w", err)
	}
	// Guard both shapes of response. A non-streaming call can stall
	// after headers exactly as a streaming one can — the body just
	// arrives through a json.Decoder instead of a scanner — so the guard
	// wraps the reader rather than the loop that consumes it.
	resp.Body = newStreamIdleGuard(resp.Body, cancelReq, c.streamIdleTimeout)
	defer resp.Body.Close()

	// The header is the better handle when a server sets one: it exists
	// on error responses too, where there is no completion object to
	// carry an id.
	headerRequestID := strings.TrimSpace(resp.Header.Get("x-request-id"))

	if resp.StatusCode != http.StatusOK {
		errBody := httpkit.ReadErrorBody(resp.Body, 4096)
		log.Error("API error",
			"status", resp.StatusCode,
			"body", errBody,
			"upstream_request_id", headerRequestID,
			"elapsed_ms", time.Since(started).Milliseconds(),
		)
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
		if headerRequestID != "" {
			result.UpstreamRequestID = headerRequestID
		}
		log.Debug("response received",
			"model", result.Model,
			"input_tokens", result.InputTokens,
			"output_tokens", result.OutputTokens,
			"tool_calls", len(result.Message.ToolCalls),
			"finish_reason", result.StopReason,
			"upstream_request_id", result.UpstreamRequestID,
			"total_ms", time.Since(started).Milliseconds(),
		)
		log.Log(ctx, llm.LevelTrace, "response content", "content", result.Message.Content)
		return result, nil
	}

	return c.handleStreaming(ctx, model, validToolNames, resp.Body, callback, streamTrace{
		log:        log,
		started:    started,
		upstreamID: headerRequestID,
	})
}

func (c *OpenAICompatClient) setAuth(req *http.Request) {
	if strings.TrimSpace(c.apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
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

// SetStreamIdleTimeout overrides how long this endpoint may go silent
// before a request is abandoned. Zero disables the guard entirely.
//
// Not safe to call concurrently with in-flight requests; intended for
// resource configuration during init, alongside SetLogger.
func (c *OpenAICompatClient) SetStreamIdleTimeout(d time.Duration) {
	if c == nil || d < 0 {
		return
	}
	c.streamIdleTimeout = d
}

// callLogger returns the logger for one request, preferring the
// request-scoped logger the caller put on the context so provider lines
// carry request_id, conversation_id, and session_id and can be grepped
// as one cycle. The client's own identity is re-applied on top, because
// the caller's logger knows the request and this client knows which
// endpoint is answering it — a line needs both to be worth reading.
func (c *OpenAICompatClient) callLogger(ctx context.Context) *slog.Logger {
	log := logging.Logger(ctx)
	if log == nil {
		log = c.logger
	}
	log = log.With("provider", c.provider)
	if c.resource != "" {
		log = log.With("resource", c.resource)
	}
	return log
}
