package llm

import (
	"log/slog"
	"time"
)

// LevelTrace is below Debug, used for wire-level payload logging.
const LevelTrace = slog.Level(-8)

// ImageContent holds a base64-encoded image for multimodal messages.
// Each provider serializes images differently (Ollama uses a flat
// base64 array, Anthropic uses typed content blocks), so the Images
// field on [Message] is excluded from default JSON marshaling.
type ImageContent struct {
	Data      string // base64-encoded image data (no data URI prefix)
	MediaType string // MIME type: "image/jpeg", "image/png", etc.
}

// PromptSection preserves the semantic sections of a system prompt so
// providers can apply transport-specific optimizations such as prompt
// caching without changing the prompt text itself.
type PromptSection struct {
	Name     string
	Content  string
	CacheTTL string // optional provider hint, for example "1h" or "5m"
}

// Message represents a chat message for the LLM.
type Message struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	Images     []ImageContent  `json:"-"` // multimodal images; marshaled per-provider
	Sections   []PromptSection `json:"-"` // system-prompt sections; provider-specific
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"` // For tool responses
}

// ToolCall represents a tool call from the model.
type ToolCall struct {
	ID       string `json:"id,omitempty"` // Provider-assigned ID (required by Anthropic for tool_result correlation)
	Function struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function"`
}

// ChatResponse is the unified response from any LLM provider.
// All fields use proper Go types — wire format conversion happens
// at provider boundaries (ollama.go, anthropic.go).
type ChatResponse struct {
	Model     string
	CreatedAt time.Time
	Message   Message
	Done      bool

	// Token usage (provider-neutral)
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
	// Per-TTL breakdown of cache-write tokens. Populated by providers
	// that return a structured cache_creation breakdown (Anthropic).
	// Zero when the provider doesn't expose the breakdown, in which
	// case callers should fall back to CacheCreationInputTokens and
	// treat the TTL mix as unknown (typically charged at the 5m rate
	// for cost estimation, since that's the default).
	CacheCreation5mInputTokens int
	CacheCreation1hInputTokens int

	// Timing (populated when available)
	TotalDuration time.Duration
	LoadDuration  time.Duration
	EvalDuration  time.Duration
}

// StreamEvent represents a single event in a streaming response.
// Consumers switch on Kind to determine what data is available.
type StreamEvent struct {
	Kind StreamEventKind

	// Token is set for KindToken events.
	Token string

	// ToolCall is set for KindToolCallStart events.
	ToolCall *ToolCall

	// ToolName and ToolResult are set for KindToolCallDone events.
	ToolName   string
	ToolResult string
	ToolError  string

	// Response is set for KindDone events (final summary).
	Response *ChatResponse

	// Data carries optional extensible metadata for events that need
	// more than the typed fields above. Used by KindLLMStart to
	// forward router decisions and context estimates.
	Data map[string]any
}

// StreamEventKind identifies the type of stream event.
type StreamEventKind int

const (
	// KindToken is an incremental text token from the model.
	KindToken StreamEventKind = iota

	// KindToolCallStart fires when the model invokes a tool.
	KindToolCallStart

	// KindToolCallDone fires when a tool execution completes.
	KindToolCallDone

	// KindDone signals the stream is complete. Response carries final metadata.
	KindDone

	// KindLLMResponse fires when an LLM response is received (before
	// tool execution begins). Response carries the model name and
	// token counts at the earliest point they become available.
	KindLLMResponse

	// KindLLMStart fires immediately before an LLM API call begins.
	// Response.Model carries the selected model name so consumers
	// can display it before the call completes.
	KindLLMStart
)

// StreamCallback receives streaming events.
// For backward compatibility, pure-text consumers can check event.Kind == KindToken.
type StreamCallback func(event StreamEvent)
