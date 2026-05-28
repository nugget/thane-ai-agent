package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// EmbeddingClient generates embeddings for semantic search.
type EmbeddingClient interface {
	Generate(ctx context.Context, text string) ([]float32, error)
}

// Tools provides fact-related tools for the agent.
type Tools struct {
	store      *Store
	embeddings EmbeddingClient
}

// NewTools creates fact tools using the given store.
func NewTools(store *Store) *Tools {
	return &Tools{store: store}
}

// SetEmbeddingClient sets the embedding client for semantic search.
func (t *Tools) SetEmbeddingClient(client EmbeddingClient) {
	t.embeddings = client
}

// RememberArgs are arguments for the remember_fact tool.
type RememberArgs struct {
	Category string   `json:"category"`           // user, home, device, routine, preference
	Key      string   `json:"key"`                // Unique identifier within category
	Value    string   `json:"value"`              // The information to remember
	Source   string   `json:"source,omitempty"`   // Where this came from
	Subjects []string `json:"subjects,omitempty"` // Subject keys (e.g., "entity:foo", "zone:bar")
	Ref      string   `json:"ref,omitempty"`      // KB-relative path (e.g., "dossiers/openclawssy.md")
}

// Remember stores a fact for later recall.
func (t *Tools) Remember(argsJSON string) (string, error) {
	var args RememberArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	if args.Category == "" {
		args.Category = "preference"
	}
	if args.Key == "" {
		return "", fmt.Errorf("key is required")
	}
	if args.Value == "" {
		return "", fmt.Errorf("value is required")
	}

	cat := Category(args.Category)
	fact, err := t.store.Set(cat, args.Key, args.Value, args.Source, 1.0, args.Subjects, args.Ref)
	if err != nil {
		return "", fmt.Errorf("store fact: %w", err)
	}

	// Generate embedding if client available
	if t.embeddings != nil {
		embText := fmt.Sprintf("%s: %s - %s", args.Category, args.Key, args.Value)
		if emb, err := t.embeddings.Generate(context.Background(), embText); err == nil {
			_ = t.store.SetEmbedding(fact.ID, emb)
		}
	}

	return fmt.Sprintf("Remembered: [%s] %s = %s", fact.Category, fact.Key, fact.Value), nil
}

// RecallArgs are arguments for the recall_fact tool.
type RecallArgs struct {
	Category string `json:"category,omitempty"` // Optional filter
	Key      string `json:"key,omitempty"`      // Specific key to recall
	Query    string `json:"query,omitempty"`    // Search term
}

// Recall retrieves facts from memory.
func (t *Tools) Recall(argsJSON string) (string, error) {
	var args RecallArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	// Specific key lookup
	if args.Category != "" && args.Key != "" {
		fact, err := t.store.Get(Category(args.Category), args.Key)
		if err != nil {
			return "Not found", nil
		}
		result := fmt.Sprintf("[%s] %s = %s (confidence: %.1f)",
			fact.Category, fact.Key, fact.Value, fact.Confidence)
		if fact.Ref != "" {
			result += fmt.Sprintf("\n  → kb:%s", fact.Ref)
		}
		return result, nil
	}

	// Category listing
	if args.Category != "" {
		facts, err := t.store.GetByCategory(Category(args.Category))
		if err != nil {
			return "", fmt.Errorf("get category: %w", err)
		}
		if len(facts) == 0 {
			return fmt.Sprintf("No facts in category '%s'", args.Category), nil
		}
		return formatFacts(facts), nil
	}

	// Search
	if args.Query != "" {
		facts, err := t.store.Search(args.Query)
		if err != nil {
			return "", fmt.Errorf("search: %w", err)
		}
		if len(facts) == 0 {
			return fmt.Sprintf("No facts matching '%s'", args.Query), nil
		}
		return formatFacts(facts), nil
	}

	// List all (summarized)
	stats := t.store.Stats()
	total, _ := stats["total"].(int)
	cats, _ := stats["categories"].(map[string]int)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Memory contains %d facts:\n", total))
	for cat, count := range cats {
		sb.WriteString(fmt.Sprintf("  - %s: %d\n", cat, count))
	}
	return sb.String(), nil
}

// ForgetArgs are arguments for the forget_fact tool.
type ForgetArgs struct {
	Category string `json:"category"`
	Key      string `json:"key"`
}

// Forget removes a fact from memory.
func (t *Tools) Forget(argsJSON string) (string, error) {
	var args ForgetArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	if args.Category == "" || args.Key == "" {
		return "", fmt.Errorf("category and key are required")
	}

	if err := t.store.Delete(Category(args.Category), args.Key); err != nil {
		return "", err
	}

	return fmt.Sprintf("Forgot: [%s] %s", args.Category, args.Key), nil
}

// SemanticRecallArgs are arguments for semantic_recall tool.
type SemanticRecallArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// SemanticRecall finds facts semantically similar to the query.
func (t *Tools) SemanticRecall(argsJSON string) (string, error) {
	var args SemanticRecallArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	if args.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	if args.Limit <= 0 {
		args.Limit = 5
	}
	if args.Limit > 20 {
		args.Limit = 20
	}

	if t.embeddings == nil {
		return "", fmt.Errorf("semantic search not available (no embedding client)")
	}

	// Generate embedding for query
	queryEmb, err := t.embeddings.Generate(context.Background(), args.Query)
	if err != nil {
		return "", fmt.Errorf("generate embedding: %w", err)
	}

	// Search
	facts, scores, err := t.store.SemanticSearch(queryEmb, args.Limit)
	if err != nil {
		return "", fmt.Errorf("semantic search: %w", err)
	}

	if len(facts) == 0 {
		return "No semantically similar facts found", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d relevant facts:\n\n", len(facts)))
	for i, f := range facts {
		sb.WriteString(fmt.Sprintf("%.2f | [%s] %s: %s\n", scores[i], f.Category, f.Key, f.Value))
	}
	return sb.String(), nil
}

func formatFacts(facts []*Fact) string {
	var sb strings.Builder
	for _, f := range facts {
		sb.WriteString(fmt.Sprintf("[%s] %s = %s\n", f.Category, f.Key, f.Value))
		if f.Ref != "" {
			sb.WriteString(fmt.Sprintf("  → kb:%s\n", f.Ref))
		}
	}
	return sb.String()
}

// GenerateMissingEmbeddings creates embeddings for facts that don't have them.
// Returns count of facts embedded.
func (t *Tools) GenerateMissingEmbeddings() (int, error) {
	if t.embeddings == nil {
		return 0, fmt.Errorf("embedding client not configured")
	}

	facts, err := t.store.GetFactsWithoutEmbeddings()
	if err != nil {
		return 0, err
	}

	count := 0
	for _, f := range facts {
		embText := fmt.Sprintf("%s: %s - %s", f.Category, f.Key, f.Value)
		emb, err := t.embeddings.Generate(context.Background(), embText)
		if err != nil {
			continue // Skip failures, don't halt
		}
		if err := t.store.SetEmbedding(f.ID, emb); err != nil {
			continue
		}
		count++
	}

	return count, nil
}
