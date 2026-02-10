package llm

import (
	"context"
	"fmt"
)

// MultiClient routes requests to the appropriate provider based on model name.
type MultiClient struct {
	clients  map[string]Client // provider name → client
	models   map[string]string // model name → provider name
	fallback Client            // default client for unknown models
}

// NewMultiClient creates a client that routes to multiple providers.
func NewMultiClient(fallback Client) *MultiClient {
	return &MultiClient{
		clients:  make(map[string]Client),
		models:   make(map[string]string),
		fallback: fallback,
	}
}

// AddProvider registers a client for a provider name.
func (m *MultiClient) AddProvider(name string, client Client) {
	m.clients[name] = client
}

// AddModel maps a model name to a provider.
func (m *MultiClient) AddModel(modelName, providerName string) {
	m.models[modelName] = providerName
}

// clientFor returns the appropriate client for a model.
func (m *MultiClient) clientFor(model string) Client {
	if provider, ok := m.models[model]; ok {
		if client, ok := m.clients[provider]; ok {
			return client
		}
	}
	return m.fallback
}

// Chat sends a request to the appropriate provider for the model.
func (m *MultiClient) Chat(ctx context.Context, model string, messages []Message, tools []map[string]any) (*ChatResponse, error) {
	client := m.clientFor(model)
	if client == nil {
		return nil, fmt.Errorf("no provider configured for model %q", model)
	}
	return client.Chat(ctx, model, messages, tools)
}

// ChatStream sends a streaming request to the appropriate provider.
func (m *MultiClient) ChatStream(ctx context.Context, model string, messages []Message, tools []map[string]any, callback StreamCallback) (*ChatResponse, error) {
	client := m.clientFor(model)
	if client == nil {
		return nil, fmt.Errorf("no provider configured for model %q", model)
	}
	return client.ChatStream(ctx, model, messages, tools, callback)
}

// Ping checks the fallback provider.
func (m *MultiClient) Ping(ctx context.Context) error {
	if m.fallback != nil {
		return m.fallback.Ping(ctx)
	}
	return fmt.Errorf("no fallback client configured")
}
