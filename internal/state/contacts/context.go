package contacts

import (
	"context"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
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
// RegisterAlwaysContextProvider.
func (p *ContextProvider) TagContext(ctx context.Context, req agentctx.ContextRequest) (string, error) {
	userMessage := req.UserMessage
	if userMessage == "" || p.embeddings == nil {
		return "", nil
	}

	embedding, err := p.embeddings.Generate(ctx, userMessage)
	if err != nil {
		return "", fmt.Errorf("embed query: %w", err)
	}

	contacts, scores, err := p.store.SemanticSearch(embedding, p.maxContacts)
	if err != nil {
		return "", fmt.Errorf("semantic search: %w", err)
	}

	if len(contacts) == 0 {
		return "", nil
	}

	var sb strings.Builder
	included := 0
	for i, c := range contacts {
		if scores[i] < p.minScore {
			continue
		}

		// Load properties for this contact.
		props, _ := p.store.GetProperties(c.ID)

		if included > 0 {
			sb.WriteString("\n")
		}

		sb.WriteString(fmt.Sprintf("**%s**", c.FormattedName))
		if c.Org != "" {
			sb.WriteString(fmt.Sprintf(" (%s)", c.Org))
		}
		if c.AISummary != "" {
			sb.WriteString(fmt.Sprintf(" — %s", c.AISummary))
		}
		if c.TrustZone != "" {
			sb.WriteString(fmt.Sprintf(" [%s]", c.TrustZone))
		}
		sb.WriteString("\n")

		for _, prop := range props {
			label := prop.Property
			if prop.Type != "" {
				label += " (" + prop.Type + ")"
			}
			sb.WriteString(fmt.Sprintf("  %s: %s\n", label, prop.Value))
		}

		included++
	}

	if included == 0 {
		return "", nil
	}

	return sb.String(), nil
}
