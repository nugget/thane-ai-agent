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
		focus, _ := args["focus"].(string)
		detailStr, _ := args["detail"].(string)

		var detail DetailLevel
		switch detailStr {
		case "summary":
			detail = DetailSummary
		case "brief":
			detail = DetailBrief
		case "", "full":
			detail = DetailFull
		default:
			return "", fmt.Errorf("media_transcript: invalid detail level %q (use full, summary, or brief)", detailStr)
		}

		result, err := c.GetTranscript(ctx, rawURL, language, focus, detail)
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
			"focus": map[string]any{
				"type":        "string",
				"description": "Optional focus topic for the summary. When provided, the summary emphasizes content related to this topic. Only used with detail \"summary\" or \"brief\".",
			},
			"detail": map[string]any{
				"type":        "string",
				"enum":        []string{"full", "summary", "brief"},
				"description": "Detail level: \"full\" returns the raw transcript (default), \"summary\" produces a map-reduce summary (~2-3K chars), \"brief\" produces a very concise summary (~500 chars).",
			},
		},
		"required": []string{"url"},
	}
}
