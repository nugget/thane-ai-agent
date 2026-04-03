package llm

import (
	"context"
	"fmt"
	"sort"
)

type route struct {
	providerName string
	modelName    string
}

// MultiClient routes requests to the appropriate provider based on model name.
type MultiClient struct {
	clients   map[string]Client   // provider/resource name → client
	routes    map[string]route    // route target → provider/resource + upstream model
	aliases   map[string]string   // alias → route target
	ambiguous map[string][]string // ambiguous alias → valid route targets
	fallback  Client              // default client for unknown models
}

// NewMultiClient creates a client that routes to multiple providers.
func NewMultiClient(fallback Client) *MultiClient {
	return &MultiClient{
		clients:   make(map[string]Client),
		routes:    make(map[string]route),
		aliases:   make(map[string]string),
		ambiguous: make(map[string][]string),
		fallback:  fallback,
	}
}

// AddProvider registers a client for a provider name.
func (m *MultiClient) AddProvider(name string, client Client) {
	m.clients[name] = client
}

// AddModel maps a model name to a provider.
func (m *MultiClient) AddModel(modelName, providerName string) {
	m.AddRoute(modelName, providerName, modelName)
}

// AddRoute maps a route target to a provider/resource and upstream
// model name.
func (m *MultiClient) AddRoute(target, providerName, modelName string) {
	m.routes[target] = route{
		providerName: providerName,
		modelName:    modelName,
	}
	m.aliases[target] = target
}

// AddAlias maps an alternate selector to a concrete route target.
func (m *MultiClient) AddAlias(alias, target string) {
	m.aliases[alias] = target
}

// MarkAmbiguous records that an alias maps to multiple route targets
// and must be qualified by the caller.
func (m *MultiClient) MarkAmbiguous(alias string, targets []string) {
	out := make([]string, len(targets))
	copy(out, targets)
	sort.Strings(out)
	m.ambiguous[alias] = out
}

func (m *MultiClient) resolve(model string) (Client, string, error) {
	target := model
	if routes, ok := m.ambiguous[model]; ok {
		return nil, "", fmt.Errorf("model %q is ambiguous; use one of %q", model, routes)
	}
	if alias, ok := m.aliases[model]; ok {
		target = alias
	}
	if r, ok := m.routes[target]; ok {
		client, ok := m.clients[r.providerName]
		if !ok {
			return nil, "", fmt.Errorf("no provider configured for route %q", target)
		}
		return client, r.modelName, nil
	}
	if m.fallback != nil {
		return m.fallback, model, nil
	}
	return nil, "", fmt.Errorf("no provider configured for model %q", model)
}

// Chat sends a request to the appropriate provider for the model.
func (m *MultiClient) Chat(ctx context.Context, model string, messages []Message, tools []map[string]any) (*ChatResponse, error) {
	client, routedModel, err := m.resolve(model)
	if err != nil {
		return nil, err
	}
	return client.Chat(ctx, routedModel, messages, tools)
}

// ChatStream sends a streaming request to the appropriate provider.
func (m *MultiClient) ChatStream(ctx context.Context, model string, messages []Message, tools []map[string]any, callback StreamCallback) (*ChatResponse, error) {
	client, routedModel, err := m.resolve(model)
	if err != nil {
		return nil, err
	}
	return client.ChatStream(ctx, routedModel, messages, tools, callback)
}

// Ping checks the fallback provider.
func (m *MultiClient) Ping(ctx context.Context) error {
	if m.fallback != nil {
		return m.fallback.Ping(ctx)
	}
	return fmt.Errorf("no fallback client configured")
}
