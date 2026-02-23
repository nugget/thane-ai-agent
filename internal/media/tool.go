package media

import (
	"context"
	"encoding/json"
	"fmt"
)

// ToolHandler returns a function compatible with the tools.Tool Handler
// signature. It wraps the Client for use as an agent tool.
func ToolHandler(c *Client) func(ctx context.Context, args map[string]any) (string, error) {
	return func(ctx context.Context, args map[string]any) (string, error) {
		rawURL, _ := args["url"].(string)
		if rawURL == "" {
			return "", fmt.Errorf("media_transcript: url is required")
		}

		language, _ := args["language"].(string)

		result, err := c.GetTranscript(ctx, rawURL, language)
		if err != nil {
			return "", err
		}

		out, err := json.Marshal(result)
		if err != nil {
			// Fallback to plain text.
			return fmt.Sprintf("Title: %s\nChannel: %s\nDuration: %s\n\n%s",
				result.Title, result.Channel, result.Duration, result.Transcript), nil
		}
		return string(out), nil
	}
}

// ToolDefinition returns the JSON Schema parameters for the media_transcript tool.
func ToolDefinition() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "Media URL (YouTube, Vimeo, podcast episode, or any yt-dlp-supported source).",
			},
			"language": map[string]any{
				"type":        "string",
				"description": "Subtitle language code (default: \"en\").",
			},
		},
		"required": []string{"url"},
	}
}
