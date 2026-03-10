// Package forge provides code forge integrations (GitHub, Gitea).
package forge

import "context"

// ContextProvider injects forge account configuration into the system
// prompt so the model knows which accounts are available without
// guessing. It implements the agent.ContextProvider interface via
// structural typing.
type ContextProvider struct {
	ctx string
}

// NewContextProvider creates a forge context provider from a
// pre-built context string (typically from [Manager.Context]).
func NewContextProvider(forgeCtx string) *ContextProvider {
	return &ContextProvider{ctx: forgeCtx}
}

// GetContext returns the forge account context block. The userMessage
// parameter is unused because forge context is static per session.
func (p *ContextProvider) GetContext(_ context.Context, _ string) (string, error) {
	return p.ctx, nil
}
