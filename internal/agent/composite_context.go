package agent

import (
	"context"
	"strings"
)

// CompositeContextProvider combines multiple context providers.
// Each provider's output is concatenated with newlines.
type CompositeContextProvider struct {
	providers []ContextProvider
}

// NewCompositeContextProvider creates a composite from multiple providers.
func NewCompositeContextProvider(providers ...ContextProvider) *CompositeContextProvider {
	return &CompositeContextProvider{providers: providers}
}

// Add appends a provider to the composite.
func (c *CompositeContextProvider) Add(provider ContextProvider) {
	if provider != nil {
		c.providers = append(c.providers, provider)
	}
}

// GetContext calls all providers and combines their output.
func (c *CompositeContextProvider) GetContext(ctx context.Context, userMessage string) (string, error) {
	var parts []string

	for _, p := range c.providers {
		content, err := p.GetContext(ctx, userMessage)
		if err != nil {
			// Log error but continue with other providers
			continue
		}
		if content != "" {
			parts = append(parts, content)
		}
	}

	return strings.Join(parts, "\n\n"), nil
}
