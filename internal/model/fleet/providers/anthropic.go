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

	providerLogger := logger.With("provider", "anthropic")
	return &AnthropicClient{
		apiKey: apiKey,
		logger: providerLogger,
		httpClient: httpkit.NewClient(
			// No global timeout — streaming responses can be long-lived.
			// Rely on ctx deadlines/cancellation for timeout control.
			httpkit.WithTimeout(0),
			httpkit.WithTransport(t),
			// Retry transient connection failures (matches the Ollama
			// and LMStudio clients) plus transient Anthropic-side HTTP
			// statuses: 429 for rate limiting, 500/502/503/504 for
			// upstream hiccups. Streaming is safe — retryTransport
			// only retries while the response body is still unread;
			// once RoundTrip returns the body to the caller, a
			// mid-stream failure propagates to the agent loop as a
			// normal error.
			httpkit.WithRetry(3, 2*time.Second),
			httpkit.WithRetryOnStatus(429, 500, 502, 503, 504),
			httpkit.WithLogger(providerLogger),
		),
	}
}

// Anthropic request/response types

type anthropicRequest struct {
	Model        string                 `json:"model"`
	Messages     []anthropicMessage     `json:"messages"`
	System       any                    `json:"system,omitempty"`
	MaxTokens    int                    `json:"max_tokens"`
	Stream       bool                   `json:"stream,omitempty"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []anthropicContent
}

type anthropicContent struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text,omitempty"`
	ID           string                 `json:"id,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Input        any                    `json:"input,omitempty"`
	ToolUseID    string                 `json:"tool_use_id,omitempty"`
	Content      string                 `json:"content,omitempty"` // for tool_result
	Source       *anthropicImageSource  `json:"source,omitempty"`  // for image content blocks
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

// anthropicImageSource describes a base64-encoded image for the
// Anthropic messages API.
type anthropicImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // "image/jpeg", "image/png", etc.
	Data      string `json:"data"`       // base64-encoded image data
}

type anthropicTool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  any                    `json:"input_schema"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
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
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	// CacheCreation breaks down cache-write tokens by TTL bucket so
	// downstream pricing can apply the correct multiplier (5m writes
	// are 1.25× base input, 1h writes are 2.0×). Older Anthropic
	// responses omit this object; callers must treat absence as
	// "unknown TTL mix" and fall back to CacheCreationInputTokens.
	CacheCreation *anthropicCacheCreation `json:"cache_creation,omitempty"`
}

// anthropicCacheCreation mirrors the response shape Anthropic returns
// under usage.cache_creation: a per-TTL breakdown of tokens that were
// written into the cache on this turn.
type anthropicCacheCreation struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens,omitempty"`
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
func (c *AnthropicClient) Chat(ctx context.Context, model string, messages []llm.Message, tools []map[string]any) (*llm.ChatResponse, error) {
	return c.ChatStream(ctx, model, messages, tools, nil)
}

// ChatStream sends a chat request, optionally streaming tokens via callback.
func (c *AnthropicClient) ChatStream(ctx context.Context, model string, messages []llm.Message, tools []map[string]any, callback llm.StreamCallback) (*llm.ChatResponse, error) {
	stream := callback != nil

	// Convert messages and extract system prompt
	anthropicMsgs, systemPrompt := convertToAnthropic(messages)
	anthropicTools := convertToolsToAnthropic(tools)
	systemPayload := anthropicSystemPayload(messages, systemPrompt)

	// Enforce Anthropic cache-breakpoint guards (≤4 total, per-model
	// minimum cached-prefix length) on the assembled blocks+tools.
	// Runs below the model minimum and excess breakpoints are silently
	// ignored by the API, so we drop them here and warn instead.
	if blocks, ok := systemPayload.([]anthropicContent); ok {
		applyCacheBreakpointGuards(blocks, anthropicTools, model, c.logger)
	}
	explicitCaching := anthropicUsesExplicitPromptCaching(systemPayload)

	c.logger.Debug("preparing request",
		"model", model,
		"messages", len(anthropicMsgs),
		"tools", len(anthropicTools),
		"stream", stream,
		"system_len", len(systemPrompt),
		"explicit_cache", explicitCaching,
	)

	req := anthropicRequest{
		Model:        model,
		Messages:     anthropicMsgs,
		System:       systemPayload,
		MaxTokens:    4096,
		Stream:       stream,
		Tools:        anthropicTools,
		CacheControl: anthropicPromptCacheControl(systemPrompt, anthropicMsgs, anthropicTools, explicitCaching),
	}

	logOutboundCacheMarkers(c.logger, &req)

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	c.logger.Log(ctx, llm.LevelTrace, "request payload", "json", string(jsonData))

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

func (c *AnthropicClient) handleNonStreaming(ctx context.Context, body io.Reader) (*llm.ChatResponse, error) {
	var resp anthropicResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	result := convertFromAnthropic(&resp)

	c.logger.Debug("response received",
		"model", result.Model,
		"input_tokens", result.InputTokens,
		"output_tokens", result.OutputTokens,
		"cache_creation_input_tokens", result.CacheCreationInputTokens,
		"cache_read_input_tokens", result.CacheReadInputTokens,
		"cache_hit_rate", result.CacheHitRate(),
		"tool_calls", len(result.Message.ToolCalls),
	)
	c.logger.Log(ctx, llm.LevelTrace, "response content", "content", result.Message.Content)

	return result, nil
}

func (c *AnthropicClient) handleStreaming(ctx context.Context, body io.Reader, callback llm.StreamCallback) (*llm.ChatResponse, error) {
	scanner := bufio.NewScanner(body)
	// Increase scanner buffer for large responses
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		contentBuilder strings.Builder
		toolCalls      []llm.ToolCall
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
						callback(llm.StreamEvent{Kind: llm.KindToken, Token: event.Delta.Text})
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
				toolCalls = append(toolCalls, llm.ToolCall{
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

	resp := &llm.ChatResponse{
		Model: model,
		Message: llm.Message{
			Role:      "assistant",
			Content:   contentBuilder.String(),
			ToolCalls: toolCalls,
		},
		Done:                     true,
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
	}
	if bd := usage.CacheCreation; bd != nil {
		resp.CacheCreation5mInputTokens = bd.Ephemeral5mInputTokens
		resp.CacheCreation1hInputTokens = bd.Ephemeral1hInputTokens
	}

	// stopReason available for future use (end_turn, tool_use, max_tokens, stop_sequence)
	_ = stopReason

	c.logger.Debug("stream complete",
		"model", resp.Model,
		"input_tokens", resp.InputTokens,
		"output_tokens", resp.OutputTokens,
		"cache_creation_input_tokens", resp.CacheCreationInputTokens,
		"cache_read_input_tokens", resp.CacheReadInputTokens,
		"cache_hit_rate", resp.CacheHitRate(),
		"content_len", len(resp.Message.Content),
		"tool_calls", len(resp.Message.ToolCalls),
	)
	c.logger.Log(ctx, llm.LevelTrace, "stream final content", "content", resp.Message.Content)

	return resp, nil
}

func anthropicPromptCacheControl(systemPrompt string, messages []anthropicMessage, tools []anthropicTool, explicit bool) *anthropicCacheControl {
	if explicit || !shouldUseAnthropicPromptCaching(systemPrompt, messages, tools) {
		return nil
	}
	return &anthropicCacheControl{Type: "ephemeral"}
}

func anthropicSystemPayload(messages []llm.Message, fallback string) any {
	for _, msg := range messages {
		if msg.Role != "system" || len(msg.Sections) == 0 {
			continue
		}
		blocks := anthropicSystemBlocks(msg.Sections)
		if len(blocks) > 0 {
			return blocks
		}
		break
	}
	return fallback
}

func anthropicSystemBlocks(sections []llm.PromptSection) []anthropicContent {
	blocks := make([]anthropicContent, 0, len(sections))
	for _, section := range sections {
		if section.Content == "" {
			continue
		}
		blocks = append(blocks, anthropicContent{
			Type: "text",
			Text: section.Content,
		})
	}
	if len(blocks) == 0 {
		return nil
	}
	for _, run := range promptCacheRuns(sections) {
		blocks[run.end].CacheControl = &anthropicCacheControl{
			Type: "ephemeral",
			TTL:  run.ttl,
		}
	}
	return blocks
}

// maxAnthropicCacheBreakpoints is the Anthropic-enforced per-request
// limit on cache_control markers across system blocks, tools, and
// messages. Exceeding it causes the API to reject the request, so
// [applyCacheBreakpointGuards] silently drops excess breakpoints and
// warns via slog rather than letting the request fail.
const maxAnthropicCacheBreakpoints = 4

// estimatedCharsPerToken is the coarse text-to-token ratio Anthropic
// publishes for English prose. Used to check per-run prefix lengths
// against the model-specific minimum cacheable tokens.
const estimatedCharsPerToken = 4

// minCacheablePrefixTokens returns the minimum token count a cached
// prefix must reach for the Anthropic API to actually cache it. Runs
// below this threshold are silently processed as uncached, which is
// indistinguishable from a cache miss in the usage response — hence
// the guard surfaces them as warnings.
//
// Values track the published per-family minimums (Sonnet 1024, Opus
// 4096, Haiku 4096). Unknown models default to the strictest minimum
// so we never advertise caching we can't confirm.
func minCacheablePrefixTokens(model string) int {
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "opus"):
		return 4096
	case strings.Contains(lower, "haiku"):
		return 4096
	case strings.Contains(lower, "sonnet"):
		return 1024
	default:
		return 4096
	}
}

// applyCacheBreakpointGuards enforces the Anthropic per-request cache
// breakpoint cap (≤4) and the per-model minimum cached-prefix length
// across system blocks and the tool cache. Both are silent-failure
// modes in the raw API: under-minimum runs are processed uncached
// without any signal, and over-the-cap breakpoint counts cause a
// request rejection.
//
// The guard policy:
//
//  1. For each system block that carries a cache_control, compute the
//     prefix length through that block. If the prefix is below the
//     model-specific minimum, strip cache_control and warn. The block
//     itself remains; only the breakpoint is removed.
//  2. Count surviving system breakpoints plus the one blanket tool
//     cache (if any). If the total still exceeds 4, drop the tool
//     cache first — it is an undifferentiated "cache the last tool"
//     policy, whereas system breakpoints reflect deliberate
//     per-section TTL choices.
//  3. If still over the cap, drop trailing system breakpoints (the
//     earliest runs typically cover the largest stable prefix, so
//     dropping from the tail minimizes the loss).
func applyCacheBreakpointGuards(blocks []anthropicContent, tools []anthropicTool, model string, logger *slog.Logger) {
	minTokens := minCacheablePrefixTokens(model)

	// Step 1: strip under-minimum breakpoints from system blocks.
	prefixChars := 0
	for i := range blocks {
		prefixChars += len(blocks[i].Text)
		if blocks[i].CacheControl == nil {
			continue
		}
		if prefixChars/estimatedCharsPerToken < minTokens {
			logger.Warn("dropping under-minimum Anthropic cache breakpoint",
				"model", model,
				"block_index", i,
				"prefix_chars", prefixChars,
				"prefix_tokens_estimate", prefixChars/estimatedCharsPerToken,
				"min_tokens", minTokens,
				"ttl", blocks[i].CacheControl.TTL,
			)
			blocks[i].CacheControl = nil
		}
	}

	// Step 2: count surviving breakpoints.
	systemBreakpoints := 0
	for i := range blocks {
		if blocks[i].CacheControl != nil {
			systemBreakpoints++
		}
	}
	toolBreakpoint := 0
	if n := len(tools); n > 0 && tools[n-1].CacheControl != nil {
		toolBreakpoint = 1
	}

	total := systemBreakpoints + toolBreakpoint
	if total <= maxAnthropicCacheBreakpoints {
		return
	}

	// Step 3: drop the tool breakpoint first.
	if toolBreakpoint > 0 {
		logger.Warn("dropping tool cache breakpoint to fit Anthropic 4-breakpoint cap",
			"system_breakpoints", systemBreakpoints,
			"total_requested", total,
			"max", maxAnthropicCacheBreakpoints,
		)
		tools[len(tools)-1].CacheControl = nil
		total--
		if total <= maxAnthropicCacheBreakpoints {
			return
		}
	}

	// Step 4: drop trailing system breakpoints until we fit.
	excess := total - maxAnthropicCacheBreakpoints
	for i := len(blocks) - 1; i >= 0 && excess > 0; i-- {
		if blocks[i].CacheControl == nil {
			continue
		}
		logger.Warn("dropping trailing system cache breakpoint to fit Anthropic 4-breakpoint cap",
			"block_index", i,
			"ttl", blocks[i].CacheControl.TTL,
		)
		blocks[i].CacheControl = nil
		excess--
	}
}

// logOutboundCacheMarkers emits a structured debug line describing every
// cache_control breakpoint that will land in the outbound request bytes.
// A 0% cache-read rate combined with a payload that omits markers points
// at a different bug than one where markers are present but the prefix
// isn't matching, so this log is the cheapest way to disambiguate.
//
// The function short-circuits when Debug is disabled so the loops and
// slice allocations don't run on every request in production.
func logOutboundCacheMarkers(logger *slog.Logger, req *anthropicRequest) {
	if logger == nil || !logger.Enabled(context.Background(), slog.LevelDebug) {
		return
	}

	systemPayloadKind := "none"
	systemBlocks := 0
	systemBreakpoints := 0
	systemTotalChars := 0
	systemTTLs := make([]string, 0, 4)
	systemBreakpointPrefixChars := make([]int, 0, 4)
	switch system := req.System.(type) {
	case []anthropicContent:
		systemPayloadKind = "blocks"
		systemBlocks = len(system)
		prefix := 0
		for _, b := range system {
			prefix += len(b.Text)
			if b.CacheControl != nil {
				systemBreakpoints++
				ttl := b.CacheControl.TTL
				if ttl == "" {
					ttl = "default"
				}
				systemTTLs = append(systemTTLs, ttl)
				systemBreakpointPrefixChars = append(systemBreakpointPrefixChars, prefix)
			}
		}
		systemTotalChars = prefix
	case string:
		// Fallback path from anthropicSystemPayload when no
		// PromptSections are present. The system content still ships,
		// just as a single un-cached string — record its size so
		// operators can correlate prefix length even without blocks.
		if system != "" {
			systemPayloadKind = "string"
			systemBlocks = 1
			systemTotalChars = len(system)
		}
	}

	toolBreakpointTTL := ""
	if n := len(req.Tools); n > 0 && req.Tools[n-1].CacheControl != nil {
		toolBreakpointTTL = req.Tools[n-1].CacheControl.TTL
		if toolBreakpointTTL == "" {
			toolBreakpointTTL = "default"
		}
	}

	requestLevelTTL := ""
	if req.CacheControl != nil {
		requestLevelTTL = req.CacheControl.TTL
		if requestLevelTTL == "" {
			requestLevelTTL = "default"
		}
	}

	logger.Debug("outbound cache markers",
		"model", req.Model,
		"system_payload_kind", systemPayloadKind,
		"system_blocks", systemBlocks,
		"system_breakpoints", systemBreakpoints,
		"system_breakpoint_ttls", systemTTLs,
		"system_breakpoint_prefix_chars", systemBreakpointPrefixChars,
		"system_total_chars", systemTotalChars,
		"tools", len(req.Tools),
		"tool_breakpoint_ttl", toolBreakpointTTL,
		"request_cache_control_ttl", requestLevelTTL,
	)
}

type promptCacheRun struct {
	ttl   string
	start int
	end   int
}

func promptCacheRuns(sections []llm.PromptSection) []promptCacheRun {
	var runs []promptCacheRun
	blockIndex := -1
	for _, section := range sections {
		if section.Content == "" {
			continue
		}
		blockIndex++
		ttl := strings.TrimSpace(section.CacheTTL)
		if ttl == "" {
			continue
		}
		if len(runs) == 0 || runs[len(runs)-1].ttl != ttl {
			runs = append(runs, promptCacheRun{ttl: ttl, start: blockIndex, end: blockIndex})
			continue
		}
		runs[len(runs)-1].end = blockIndex
	}
	return runs
}

func anthropicUsesExplicitPromptCaching(system any) bool {
	switch blocks := system.(type) {
	case []anthropicContent:
		for _, block := range blocks {
			if block.CacheControl != nil {
				return true
			}
		}
	}
	return false
}

func shouldUseAnthropicPromptCaching(systemPrompt string, messages []anthropicMessage, tools []anthropicTool) bool {
	if len(tools) > 0 {
		return true
	}
	if len(messages) >= 3 {
		return true
	}
	if strings.TrimSpace(systemPrompt) != "" && len(systemPrompt) >= 4096 {
		return true
	}
	for _, msg := range messages {
		if msg.Role == "assistant" {
			return true
		}
	}
	return false
}

// convertToAnthropic converts internal messages to Anthropic format.
// Extracts system messages into a separate system prompt.
func convertToAnthropic(messages []llm.Message) ([]anthropicMessage, string) {
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
			if len(msg.Images) > 0 {
				// Multimodal: image content blocks followed by text.
				var blocks []anthropicContent
				for _, img := range msg.Images {
					blocks = append(blocks, anthropicContent{
						Type: "image",
						Source: &anthropicImageSource{
							Type:      "base64",
							MediaType: img.MediaType,
							Data:      img.Data,
						},
					})
				}
				if msg.Content != "" {
					blocks = append(blocks, anthropicContent{
						Type: "text",
						Text: msg.Content,
					})
				}
				result = append(result, anthropicMessage{
					Role:    "user",
					Content: blocks,
				})
			} else {
				result = append(result, anthropicMessage{
					Role:    "user",
					Content: msg.Content,
				})
			}
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
		} else if schema, ok := params.(map[string]any); ok {
			params, _ = llm.StripTopLevelCompositionKeywords(schema)
		}

		result = append(result, anthropicTool{
			Name:        name,
			Description: desc,
			InputSchema: params,
		})
	}
	if len(result) > 0 {
		result[len(result)-1].CacheControl = &anthropicCacheControl{Type: "ephemeral", TTL: "1h"}
	}
	return result
}

// convertFromAnthropic converts an Anthropic response to our internal format.
func convertFromAnthropic(resp *anthropicResponse) *llm.ChatResponse {
	var content string
	var toolCalls []llm.ToolCall

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			content += block.Text
		case "tool_use":
			args, ok := block.Input.(map[string]any)
			if !ok {
				args = map[string]any{}
			}
			toolCalls = append(toolCalls, llm.ToolCall{
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

	out := &llm.ChatResponse{
		Model: resp.Model,
		Message: llm.Message{
			Role:      resp.Role,
			Content:   content,
			ToolCalls: toolCalls,
		},
		Done:                     true,
		InputTokens:              resp.Usage.InputTokens,
		OutputTokens:             resp.Usage.OutputTokens,
		CacheCreationInputTokens: resp.Usage.CacheCreationInputTokens,
		CacheReadInputTokens:     resp.Usage.CacheReadInputTokens,
	}
	if bd := resp.Usage.CacheCreation; bd != nil {
		out.CacheCreation5mInputTokens = bd.Ephemeral5mInputTokens
		out.CacheCreation1hInputTokens = bd.Ephemeral1hInputTokens
	}
	return out
}

// (toolUseID removed — IDs are now carried on ToolCall.ID directly)
