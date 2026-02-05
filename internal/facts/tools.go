package facts

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Tools provides fact-related tools for the agent.
type Tools struct {
	store *Store
}

// NewTools creates fact tools using the given store.
func NewTools(store *Store) *Tools {
	return &Tools{store: store}
}

// RememberArgs are arguments for the remember_fact tool.
type RememberArgs struct {
	Category string `json:"category"`         // user, home, device, routine, preference
	Key      string `json:"key"`              // Unique identifier within category
	Value    string `json:"value"`            // The information to remember
	Source   string `json:"source,omitempty"` // Where this came from
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
	fact, err := t.store.Set(cat, args.Key, args.Value, args.Source, 1.0)
	if err != nil {
		return "", fmt.Errorf("store fact: %w", err)
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
		return fmt.Sprintf("[%s] %s = %s (confidence: %.1f)",
			fact.Category, fact.Key, fact.Value, fact.Confidence), nil
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

// GetDefinitions returns tool definitions for the fact tools.
func (t *Tools) GetDefinitions() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "remember_fact",
				"description": "Store a piece of information for later recall. Use for user preferences, home layout, device mappings, or observed patterns.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"category": map[string]any{
							"type":        "string",
							"enum":        []string{"user", "home", "device", "routine", "preference"},
							"description": "Category for organizing the fact",
						},
						"key": map[string]any{
							"type":        "string",
							"description": "Unique identifier for this fact within the category (e.g., 'time_format', 'bedroom_light')",
						},
						"value": map[string]any{
							"type":        "string",
							"description": "The information to remember",
						},
						"source": map[string]any{
							"type":        "string",
							"description": "Where this information came from (e.g., 'user stated', 'observed')",
						},
					},
					"required": []string{"key", "value"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "recall_fact",
				"description": "Retrieve information from long-term memory. Can look up specific facts, list a category, or search.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"category": map[string]any{
							"type":        "string",
							"description": "Category to filter by",
						},
						"key": map[string]any{
							"type":        "string",
							"description": "Specific key to recall (requires category)",
						},
						"query": map[string]any{
							"type":        "string",
							"description": "Search term to find matching facts",
						},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "forget_fact",
				"description": "Remove a fact from long-term memory.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"category": map[string]any{
							"type":        "string",
							"description": "Category of the fact to forget",
						},
						"key": map[string]any{
							"type":        "string",
							"description": "Key of the fact to forget",
						},
					},
					"required": []string{"category", "key"},
				},
			},
		},
	}
}

func formatFacts(facts []*Fact) string {
	var sb strings.Builder
	for _, f := range facts {
		sb.WriteString(fmt.Sprintf("[%s] %s = %s\n", f.Category, f.Key, f.Value))
	}
	return sb.String()
}
