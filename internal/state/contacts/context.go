package contacts

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/contacts/contextfmt"
)

// ContextProvider provides relevant contacts as context for the system prompt.
type ContextProvider struct {
	store       *Store
	embeddings  EmbeddingClient
	maxContacts int
	minScore    float32
}

// NewContextProvider creates a context provider. A nil embeddings client
// is handled gracefully — TagContext returns empty context in that case.
func NewContextProvider(store *Store, embeddings EmbeddingClient) *ContextProvider {
	return &ContextProvider{
		store:       store,
		embeddings:  embeddings,
		maxContacts: 5,
		minScore:    0.3,
	}
}

// TagContextBucket places semantic contact matches in related context
// because they are selected from the current user message.
func (p *ContextProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketRelated
}

// SetMaxContacts configures how many contacts to include. Values less
// than 1 are clamped to 1.
func (p *ContextProvider) SetMaxContacts(n int) {
	if n < 1 {
		n = 1
	}
	p.maxContacts = n
}

// SetMinScore configures the minimum similarity threshold.
func (p *ContextProvider) SetMinScore(score float32) {
	p.minScore = score
}

// TagContext returns relevant contacts formatted for the system prompt.
// Implements [agent.TagContextProvider]; registered via
// RegisterAlwaysContextProvider. The body is rendered by
// [contextfmt.Format] as compact JSON under a markdown heading.
func (p *ContextProvider) TagContext(ctx context.Context, req agentctx.ContextRequest) (string, error) {
	userMessage := req.UserMessage
	if userMessage == "" || p.embeddings == nil {
		return "", nil
	}

	embedding, err := p.embeddings.Generate(ctx, userMessage)
	if err != nil {
		return "", fmt.Errorf("embed query: %w", err)
	}

	contactsList, scores, err := p.store.SemanticSearch(embedding, p.maxContacts)
	if err != nil {
		return "", fmt.Errorf("semantic search: %w", err)
	}

	matches := make([]contextfmt.Match, 0, len(contactsList))
	for i, c := range contactsList {
		if scores[i] < p.minScore {
			continue
		}

		props, _ := p.store.GetProperties(c.ID)
		viewProps := make([]contextfmt.Property, 0, len(props))
		for _, prop := range props {
			viewProps = append(viewProps, contextfmt.Property{
				Label: prop.Property,
				Type:  prop.Type,
				Value: prop.Value,
			})
		}

		matches = append(matches, contextfmt.Match{
			Name:       c.FormattedName,
			Org:        c.Org,
			Summary:    c.AISummary,
			TrustZone:  c.TrustZone,
			Score:      scores[i],
			Properties: viewProps,
		})
	}

	return contextfmt.Format(matches), nil
}
