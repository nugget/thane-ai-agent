package contacts

import (
	"context"
	"fmt"
	"strings"
)

// ContextProvider provides relevant contacts as context for the system prompt.
type ContextProvider struct {
	store       *Store
	embeddings  EmbeddingClient
	maxContacts int
	minScore    float32
}

// NewContextProvider creates a context provider. A nil embeddings client
// is handled gracefully — GetContext returns empty context in that case.
func NewContextProvider(store *Store, embeddings EmbeddingClient) *ContextProvider {
	return &ContextProvider{
		store:       store,
		embeddings:  embeddings,
		maxContacts: 5,
		minScore:    0.3,
	}
}

// SetMaxContacts configures how many contacts to include.
func (p *ContextProvider) SetMaxContacts(n int) {
	p.maxContacts = n
}

// SetMinScore configures the minimum similarity threshold.
func (p *ContextProvider) SetMinScore(score float32) {
	p.minScore = score
}

// GetContext returns relevant contacts formatted for the system prompt.
// Implements agent.ContextProvider interface.
func (p *ContextProvider) GetContext(ctx context.Context, userMessage string) (string, error) {
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

		// Load facts for this contact.
		facts, _ := p.store.GetFacts(c.ID)

		if included > 0 {
			sb.WriteString("\n")
		}

		sb.WriteString(fmt.Sprintf("**%s**", c.Name))
		if c.Relationship != "" {
			sb.WriteString(fmt.Sprintf(" (%s)", c.Relationship))
		}
		if c.Summary != "" {
			sb.WriteString(fmt.Sprintf(" — %s", c.Summary))
		}
		sb.WriteString("\n")

		if len(facts) > 0 {
			parts := make([]string, 0, len(facts))
			for k, v := range facts {
				parts = append(parts, fmt.Sprintf("%s: %s", k, v))
			}
			sb.WriteString("  " + strings.Join(parts, " | ") + "\n")
		}

		included++
	}

	if included == 0 {
		return "", nil
	}

	return sb.String(), nil
}
