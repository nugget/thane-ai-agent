package anticipation

import (
	"context"
	"sync"
	"time"
)

// Provider implements agent.ContextProvider for anticipation context injection.
// It checks for matching anticipations on each agent wake and injects relevant
// context into the system prompt. All methods are safe for concurrent use.
type Provider struct {
	store *Store

	mu          sync.RWMutex
	lastContext WakeContext // Context from the most recent wake event
}

// NewProvider creates an anticipation context provider.
func NewProvider(store *Store) *Provider {
	return &Provider{store: store}
}

// SetWakeContext updates the current wake context for matching.
// Call this before processing a message when you have event context.
func (p *Provider) SetWakeContext(ctx WakeContext) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastContext = ctx
}

// GetContext implements agent.ContextProvider.
// Returns formatted anticipation context for any matching active anticipations.
func (p *Provider) GetContext(ctx context.Context, userMessage string) (string, error) {
	// Build wake context - use provided context or create minimal one
	p.mu.RLock()
	wakeCtx := p.lastContext
	p.mu.RUnlock()

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
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastContext = WakeContext{}
}
