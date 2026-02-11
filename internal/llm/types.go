// Package llm provides LLM client implementations.
package llm

import (
	"log/slog"
	"time"
)

// LevelTrace is below Debug, used for wire-level payload logging.
const LevelTrace = slog.Level(-8)

// Message represents a chat message for the LLM.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // For tool responses
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
	InputTokens  int
	OutputTokens int

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
)

// StreamCallback receives streaming events.
// For backward compatibility, pure-text consumers can check event.Kind == KindToken.
type StreamCallback func(event StreamEvent)
