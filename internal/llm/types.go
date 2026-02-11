// Package llm provides LLM client implementations.
package llm

import "time"

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

// StreamCallback is called for each streamed token.
type StreamCallback func(token string)
