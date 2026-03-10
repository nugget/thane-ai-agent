package media

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/opstate"
	"github.com/nugget/thane-ai-agent/internal/paths"
)

// AnalysisTools provides tool handlers for persisting media analysis
// to an Obsidian-compatible vault and tracking engagement.
type AnalysisTools struct {
	state             *opstate.Store
	store             *MediaStore
	writer            *VaultWriter
	defaultOutputPath string
	logger            *slog.Logger
}

// NewAnalysisTools creates analysis tool handlers. The defaultOutputPath
// is used when a feed has no per-feed output_path configured.
func NewAnalysisTools(
	state *opstate.Store,
	store *MediaStore,
	writer *VaultWriter,
	defaultOutputPath string,
	logger *slog.Logger,
) *AnalysisTools {
	if logger == nil {
		logger = slog.Default()
	}
	return &AnalysisTools{
		state:             state,
		store:             store,
		writer:            writer,
		defaultOutputPath: defaultOutputPath,
		logger:            logger,
	}
}

// SaveDefinition returns the JSON Schema for the media_save_analysis tool.
func SaveDefinition() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{
				"type":        "string",
				"description": "Title for the analysis (typically the media title).",
			},
			"channel": map[string]any{
				"type":        "string",
				"description": "Channel or source name (used for directory grouping).",
			},
			"url": map[string]any{
				"type":        "string",
				"description": "URL of the analyzed media entry.",
			},
			"published": map[string]any{
				"type":        "string",
				"description": "Publication date in YYYY-MM-DD format. Falls back to today if omitted.",
			},
			"topics": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Topic tags extracted from the content (e.g., [\"ai\", \"robotics\"]).",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The analysis markdown body. Structure based on trust zone: use '## Key Facts' for trusted, '## Claims (requires corroboration)' for known, or '## Topics & Insights' for unknown feeds.",
			},
			"feed_id": map[string]any{
				"type":        "string",
				"description": "Feed ID from the wake message. Used to resolve per-feed output_path and trust_zone.",
			},
			"trust_zone": map[string]any{
				"type":        "string",
				"enum":        []string{"trusted", "known", "unknown"},
				"description": "Trust zone override. If omitted, resolved from the feed's trust_zone setting.",
			},
			"quality_score": map[string]any{
				"type":        "number",
				"description": "Quality rating from 0 to 1 (optional).",
			},
			"detail": map[string]any{
				"type":        "string",
				"enum":        []string{"brief", "summary", "full"},
				"description": "Analysis depth level (optional, recorded for engagement tracking).",
			},
		},
		"required": []string{"title", "channel", "url", "topics", "content"},
	}
}

// SaveHandler returns the tool handler for media_save_analysis.
func (at *AnalysisTools) SaveHandler() func(ctx context.Context, args map[string]any) (string, error) {
	return func(ctx context.Context, args map[string]any) (string, error) {
		// Extract required parameters.
		title, _ := args["title"].(string)
		if title == "" {
			return "", fmt.Errorf("media_save_analysis: title is required")
		}
		channel, _ := args["channel"].(string)
		if channel == "" {
			return "", fmt.Errorf("media_save_analysis: channel is required")
		}
		entryURL, _ := args["url"].(string)
		if entryURL == "" {
			return "", fmt.Errorf("media_save_analysis: url is required")
		}
		content, _ := args["content"].(string)
		if content == "" {
			return "", fmt.Errorf("media_save_analysis: content is required")
		}

		// Topics: required but may be empty array.
		topics, err := extractTopics(args)
		if err != nil {
			return "", fmt.Errorf("media_save_analysis: %w", err)
		}

		// Optional parameters.
		published, _ := args["published"].(string)
		feedIDParam, _ := args["feed_id"].(string)
		trustZone, _ := args["trust_zone"].(string)
		detail, _ := args["detail"].(string)

		var qualityScore float64
		if qs, ok := args["quality_score"].(float64); ok {
			if qs < 0 || qs > 1 {
				return "", fmt.Errorf("media_save_analysis: quality_score must be between 0 and 1")
			}
			qualityScore = qs
		}

		// Resolve trust_zone from feed if not explicitly provided.
		if trustZone == "" && feedIDParam != "" {
			tz, _ := at.state.Get(feedNamespace, feedKeyTrustZone(feedIDParam))
			trustZone = tz
		}
		if trustZone == "" {
			trustZone = "unknown"
		}

		// Resolve output path: feed-specific → config default → error.
		outputPath := at.resolveOutputPath(feedIDParam)
		if outputPath == "" {
			return "", fmt.Errorf("media_save_analysis: no output_path configured (set per-feed output_path via media_follow or configure media.analysis.default_output_path)")
		}

		// Check for duplicate analysis.
		if at.store != nil {
			analyzed, err := at.store.HasBeenAnalyzed(ctx, entryURL)
			if err != nil {
				at.logger.Warn("engagement dedup check failed", "url", entryURL, "error", err)
			} else if analyzed {
				return `{"status": "already_analyzed", "message": "This URL has already been analyzed"}`, nil
			}
		}

		// Write analysis to vault.
		now := time.Now().UTC()
		page := &AnalysisPage{
			Title:        title,
			Channel:      channel,
			URL:          entryURL,
			Published:    published,
			Topics:       topics,
			TrustZone:    trustZone,
			QualityScore: qualityScore,
			AnalyzedAt:   now,
			Content:      content,
		}

		filePath, err := at.writer.WriteAnalysis(outputPath, page)
		if err != nil {
			return "", fmt.Errorf("media_save_analysis: %w", err)
		}

		// Record engagement.
		if at.store != nil {
			eng := &Engagement{
				EntryURL:      entryURL,
				FeedID:        feedIDParam,
				AnalysisPath:  filePath,
				AnalysisDepth: detail,
				Topics:        topics,
				TrustZone:     trustZone,
				QualityScore:  qualityScore,
				AnalyzedAt:    now,
			}
			if err := at.store.RecordAnalysis(ctx, eng); err != nil {
				at.logger.Warn("failed to record engagement",
					"url", entryURL,
					"error", err,
				)
			}
		}

		at.logger.Info("media analysis saved",
			"path", filePath,
			"url", entryURL,
			"channel", channel,
			"trust_zone", trustZone,
		)

		result := map[string]string{
			"status": "saved",
			"path":   filePath,
		}
		out, _ := json.Marshal(result)
		return string(out), nil
	}
}

// resolveOutputPath determines the output directory for analysis files.
// Resolution order: per-feed output_path → config default. Both support
// ~ expansion.
func (at *AnalysisTools) resolveOutputPath(feedID string) string {
	// Try per-feed output_path.
	if feedID != "" {
		if p, _ := at.state.Get(feedNamespace, feedKeyOutputPath(feedID)); p != "" {
			return paths.ExpandHome(p)
		}
	}

	// Fall back to config default.
	if at.defaultOutputPath != "" {
		return paths.ExpandHome(at.defaultOutputPath)
	}

	return ""
}

// extractTopics parses the topics parameter from tool arguments.
func extractTopics(args map[string]any) ([]string, error) {
	raw, ok := args["topics"]
	if !ok {
		return nil, fmt.Errorf("topics is required")
	}

	switch v := raw.(type) {
	case []any:
		topics := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("topics must be an array of strings")
			}
			topics = append(topics, s)
		}
		return topics, nil
	case []string:
		return v, nil
	default:
		return nil, fmt.Errorf("topics must be an array of strings")
	}
}

// feedKeyOutputPath returns the opstate key for a feed's output path.
func feedKeyOutputPath(id string) string { return "feed:" + id + ":output_path" }
