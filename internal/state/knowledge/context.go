package knowledge

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/knowledge/contextfmt"
)

// ContextProvider provides relevant facts as context for the system prompt.
type ContextProvider struct {
	store    *Store
	embedder *Client
	maxFacts int
	minScore float32
}

// NewContextProvider creates a context provider.
func NewContextProvider(store *Store, embedder *Client) *ContextProvider {
	return &ContextProvider{
		store:    store,
		embedder: embedder,
		maxFacts: 5,   // Return top 5 most relevant facts
		minScore: 0.3, // Minimum similarity score
	}
}

// TagContextBucket places semantic fact matches in related context
// because they are selected from the current user message.
func (p *ContextProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketRelated
}

// SetMaxFacts configures how many facts to include.
func (p *ContextProvider) SetMaxFacts(n int) {
	p.maxFacts = n
}

// SetMinScore configures the minimum similarity threshold.
func (p *ContextProvider) SetMinScore(score float32) {
	p.minScore = score
}

// TagContext returns relevant facts formatted for the system prompt.
// Implements [agent.TagContextProvider]; registered via
// RegisterAlwaysContextProvider. The body is rendered by
// [contextfmt.FormatSimilarity] as compact JSON under a markdown heading.
func (p *ContextProvider) TagContext(ctx context.Context, req agentctx.ContextRequest) (string, error) {
	userMessage := req.UserMessage
	if userMessage == "" {
		return "", nil
	}

	embedding, err := p.embedder.Generate(ctx, userMessage)
	if err != nil {
		return "", fmt.Errorf("embed query: %w", err)
	}

	facts, scores, err := p.store.SemanticSearch(embedding, p.maxFacts)
	if err != nil {
		return "", fmt.Errorf("semantic search: %w", err)
	}

	views := make([]contextfmt.SimilarityFact, 0, len(facts))
	for i, f := range facts {
		if scores[i] < p.minScore {
			continue
		}
		views = append(views, contextfmt.SimilarityFact{
			Category: string(f.Category),
			Key:      f.Key,
			Value:    f.Value,
			Score:    scores[i],
		})
	}

	return contextfmt.FormatSimilarity(views), nil
}
