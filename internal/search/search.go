// Package search provides a pluggable web search interface for the agent.
//
// Each search provider implements the [Provider] interface and is
// registered by name. The [Manager] selects a provider based on
// configuration and exposes a single [Manager.Search] method that
// the tool layer calls.
package search

import (
	"context"
	"fmt"
)

// Result is a single search result.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// Options are optional parameters for a search query.
type Options struct {
	// Count is the maximum number of results to return.
	// Providers may return fewer. Zero means provider default.
	Count int `json:"count,omitempty"`

	// Language is an ISO 639-1 language code (e.g., "en", "de").
	Language string `json:"language,omitempty"`
}

// Provider is the interface that search backends implement.
type Provider interface {
	// Name returns the provider identifier (e.g., "searxng", "brave").
	Name() string

	// Search executes a query and returns results.
	Search(ctx context.Context, query string, opts Options) ([]Result, error)
}

// Manager holds configured providers and routes searches.
type Manager struct {
	providers map[string]Provider
	primary   string
}

// NewManager creates a search manager. The primary provider name
// determines which backend is used by default.
func NewManager(primary string) *Manager {
	return &Manager{
		providers: make(map[string]Provider),
		primary:   primary,
	}
}

// Register adds a provider to the manager.
func (m *Manager) Register(p Provider) {
	m.providers[p.Name()] = p
}

// Search runs a query against the primary provider.
func (m *Manager) Search(ctx context.Context, query string, opts Options) ([]Result, error) {
	p, ok := m.providers[m.primary]
	if !ok {
		return nil, fmt.Errorf("search provider %q not configured", m.primary)
	}
	return p.Search(ctx, query, opts)
}

// SearchWith runs a query against a specific named provider.
func (m *Manager) SearchWith(ctx context.Context, provider, query string, opts Options) ([]Result, error) {
	p, ok := m.providers[provider]
	if !ok {
		return nil, fmt.Errorf("search provider %q not configured", provider)
	}
	return p.Search(ctx, query, opts)
}

// Providers returns the names of all registered providers.
func (m *Manager) Providers() []string {
	names := make([]string, 0, len(m.providers))
	for name := range m.providers {
		names = append(names, name)
	}
	return names
}

// Configured reports whether at least one provider is registered.
func (m *Manager) Configured() bool {
	return len(m.providers) > 0
}
