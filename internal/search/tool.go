package search

import (
	"context"
	"encoding/json"
	"fmt"
)

// ToolHandler returns a function compatible with the tools.Tool Handler
// signature. It wraps the Manager's search method for use as an agent tool.
func ToolHandler(mgr *Manager) func(ctx context.Context, args map[string]any) (string, error) {
	return func(ctx context.Context, args map[string]any) (string, error) {
		query, _ := args["query"].(string)
		if query == "" {
			return "", fmt.Errorf("web_search: query is required")
		}

		opts := Options{}

		if count, ok := args["count"].(float64); ok && count > 0 {
			opts.Count = int(count)
		}
		if lang, ok := args["language"].(string); ok {
			opts.Language = lang
		}

		// Allow explicit provider selection, fall back to primary.
		var results []Result
		var err error
		if provider, ok := args["provider"].(string); ok && provider != "" {
			results, err = mgr.SearchWith(ctx, provider, query, opts)
		} else {
			results, err = mgr.Search(ctx, query, opts)
		}
		if err != nil {
			return "", err
		}

		// Return JSON for structured consumption by the agent.
		out, err := json.Marshal(results)
		if err != nil {
			return FormatResults(results, len(results)), nil
		}
		return string(out), nil
	}
}

// ToolDefinition returns the JSON Schema parameters for the web_search tool.
func ToolDefinition() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The search query string.",
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "Maximum number of results to return (1-10). Default: 5.",
			},
			"language": map[string]any{
				"type":        "string",
				"description": "ISO 639-1 language code for results (e.g., 'en', 'de').",
			},
			"provider": map[string]any{
				"type":        "string",
				"description": "Search provider to use. Omit for default.",
			},
		},
		"required": []string{"query"},
	}
}
