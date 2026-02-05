// Package facts provides context injection for the agent loop.
package facts

import (
	"context"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/embeddings"
)

// ContextProvider provides relevant facts as context for the system prompt.
type ContextProvider struct {
	store    *Store
	embedder *embeddings.Client
	maxFacts int
	minScore float32
}

// NewContextProvider creates a context provider.
func NewContextProvider(store *Store, embedder *embeddings.Client) *ContextProvider {
	return &ContextProvider{
		store:    store,
		embedder: embedder,
		maxFacts: 5,   // Return top 5 most relevant facts
		minScore: 0.3, // Minimum similarity score
	}
}

// SetMaxFacts configures how many facts to include.
func (p *ContextProvider) SetMaxFacts(n int) {
	p.maxFacts = n
}

// SetMinScore configures the minimum similarity threshold.
func (p *ContextProvider) SetMinScore(score float32) {
	p.minScore = score
}

// GetContext returns relevant facts formatted for the system prompt.
// Implements agent.ContextProvider interface.
func (p *ContextProvider) GetContext(ctx context.Context, userMessage string) (string, error) {
	if userMessage == "" {
		return "", nil
	}

	// Generate embedding for user message
	embedding, err := p.embedder.Generate(ctx, userMessage)
	if err != nil {
		return "", fmt.Errorf("embed query: %w", err)
	}

	// Search for similar facts
	facts, scores, err := p.store.SemanticSearch(embedding, p.maxFacts)
	if err != nil {
		return "", fmt.Errorf("semantic search: %w", err)
	}

	if len(facts) == 0 {
		return "", nil
	}

	// Filter by minimum score and format
	var sb strings.Builder
	included := 0
	for i, f := range facts {
		if scores[i] < p.minScore {
			continue
		}
		if included > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(fmt.Sprintf("**%s/%s** (%.0f%% relevant)\n%s",
			f.Category, f.Key, scores[i]*100, f.Value))
		included++
	}

	if included == 0 {
		return "", nil
	}

	return sb.String(), nil
}
