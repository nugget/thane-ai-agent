// Package llm provides LLM client implementations.
package llm

import "context"

// Client is the interface that all LLM providers must implement.
type Client interface {
	// Chat sends a chat completion request and returns the response.
	Chat(ctx context.Context, model string, messages []Message, tools []map[string]any) (*ChatResponse, error)

	// ChatStream sends a streaming chat request. If callback is non-nil, tokens are streamed to it.
	ChatStream(ctx context.Context, model string, messages []Message, tools []map[string]any, callback StreamCallback) (*ChatResponse, error)

	// Ping checks if the provider is reachable.
	Ping(ctx context.Context) error
}
