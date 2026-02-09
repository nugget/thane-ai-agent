package anticipation

import (
	"context"
	"time"
)

// Provider implements agent.ContextProvider for anticipation context injection.
// It checks for matching anticipations on each agent wake and injects relevant
// context into the system prompt.
type Provider struct {
	store       *Store
	lastContext WakeContext // Context from the most recent wake event
}

// NewProvider creates an anticipation context provider.
func NewProvider(store *Store) *Provider {
	return &Provider{store: store}
}

// SetWakeContext updates the current wake context for matching.
// Call this before processing a message when you have event context.
func (p *Provider) SetWakeContext(ctx WakeContext) {
	p.lastContext = ctx
}

// GetContext implements agent.ContextProvider.
// Returns formatted anticipation context for any matching active anticipations.
func (p *Provider) GetContext(ctx context.Context, userMessage string) (string, error) {
	// Build wake context - use provided context or create minimal one
	wakeCtx := p.lastContext
	if wakeCtx.Time.IsZero() {
		wakeCtx.Time = time.Now()
	}

	// Find matching anticipations
	matched, err := p.store.Match(wakeCtx)
	if err != nil {
		return "", err
	}

	if len(matched) == 0 {
		return "", nil
	}

	return FormatMatchedContext(matched), nil
}

// ClearWakeContext resets the wake context after processing.
func (p *Provider) ClearWakeContext() {
	p.lastContext = WakeContext{}
}
