package fetch

import (
	"context"
	"encoding/json"
	"fmt"
)

// ToolHandler returns a function compatible with the tools.Tool Handler
// signature. It wraps the Fetcher for use as an agent tool.
func ToolHandler(f *Fetcher) func(ctx context.Context, args map[string]any) (string, error) {
	return func(ctx context.Context, args map[string]any) (string, error) {
		url, _ := args["url"].(string)
		if url == "" {
			return "", fmt.Errorf("web_fetch: url is required")
		}

		maxChars := 0
		if mc, ok := args["max_chars"].(float64); ok && mc > 0 {
			maxChars = int(mc)
		}

		result, err := f.Fetch(ctx, url, maxChars)
		if err != nil {
			return "", err
		}

		// Return JSON for structured consumption by the agent.
		out, err := json.Marshal(result)
		if err != nil {
			// Fallback to plain text
			return fmt.Sprintf("Title: %s\n\n%s", result.Title, result.Content), nil
		}
		return string(out), nil
	}
}

// ToolDefinition returns the JSON Schema parameters for the web_fetch tool.
func ToolDefinition() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "URL to fetch and extract content from.",
			},
			"max_chars": map[string]any{
				"type":        "integer",
				"description": "Maximum characters to return. Default: 50000.",
			},
		},
		"required": []string{"url"},
	}
}
