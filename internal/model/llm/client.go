// Package llm provides LLM client implementations.
package llm

import "context"

// Client is the interface that all LLM providers must implement.
type Client interface {
	// Chat sends a chat completion request and returns the response.
	// A non-nil response accompanying an error is partial, not successful
	// content. Its reported usage is still valid for accounting. Clients
	// must not invent usage when the provider supplies none.
	Chat(ctx context.Context, model string, messages []Message, tools []map[string]any) (*ChatResponse, error)

	// ChatStream sends a streaming chat request. If callback is non-nil, tokens are streamed to it.
	// The same partial-response accounting contract as Chat applies.
	ChatStream(ctx context.Context, model string, messages []Message, tools []map[string]any, callback StreamCallback) (*ChatResponse, error)

	// Ping checks if the provider is reachable.
	Ping(ctx context.Context) error
}

// ReadyWatcher is satisfied by connection watchers that can report
// whether a provider resource is currently reachable.
type ReadyWatcher interface {
	IsReady() bool
}
