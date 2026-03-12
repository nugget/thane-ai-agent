package attachments

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Tools provides agent tool handlers for attachment operations.
// The analyzer is optional — when nil, describe operations return
// cached descriptions only and cannot trigger new analysis.
type Tools struct {
	store    *Store
	analyzer *Analyzer
}

// NewTools creates attachment tool handlers backed by the given store
// and optional vision analyzer.
func NewTools(store *Store, analyzer *Analyzer) *Tools {
	return &Tools{store: store, analyzer: analyzer}
}

// attachmentSummary is the JSON-serializable summary returned by list
// and search operations.
type attachmentSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Channel     string `json:"channel,omitempty"`
	Sender      string `json:"sender,omitempty"`
	ReceivedAt  string `json:"received_at"`
	Description string `json:"description,omitempty"`
}

func summarizeRecord(rec *Record) attachmentSummary {
	desc := rec.Description
	// Truncate long descriptions in list view.
	if len(desc) > 200 {
		desc = desc[:197] + "..."
	}
	return attachmentSummary{
		ID:          rec.ID,
		Name:        rec.OriginalName,
		ContentType: rec.ContentType,
		Size:        rec.Size,
		Channel:     rec.Channel,
		Sender:      rec.Sender,
		ReceivedAt:  rec.ReceivedAt.Format("2006-01-02T15:04:05Z"),
		Description: desc,
	}
}

// List returns a formatted list of attachments matching the given filters.
func (t *Tools) List(ctx context.Context, args map[string]any) (string, error) {
	params := SearchParams{}
	if v, ok := args["conversation_id"].(string); ok {
		params.ConversationID = v
	}
	if v, ok := args["channel"].(string); ok {
		params.Channel = v
	}
	if v, ok := args["sender"].(string); ok {
		params.Sender = v
	}
	if v, ok := args["content_type"].(string); ok {
		params.ContentType = v
	}
	if v, ok := args["limit"].(float64); ok && v > 0 {
		params.Limit = int(v)
	}

	records, err := t.store.Search(ctx, params)
	if err != nil {
		return "", fmt.Errorf("attachment_list: %w", err)
	}
	if len(records) == 0 {
		return "No attachments found matching the given filters.", nil
	}

	summaries := make([]attachmentSummary, len(records))
	for i, rec := range records {
		summaries[i] = summarizeRecord(rec)
	}

	data, err := json.Marshal(summaries)
	if err != nil {
		return "", fmt.Errorf("attachment_list: marshal: %w", err)
	}
	return string(data), nil
}

// Describe returns the vision description for an attachment. When the
// attachment has not been analyzed and a vision analyzer is available,
// analysis is triggered automatically. Supports re-analysis with
// optional model and prompt overrides.
func (t *Tools) Describe(ctx context.Context, args map[string]any) (string, error) {
	id, _ := args["id"].(string)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}

	rec, err := t.store.ByID(ctx, id)
	if err != nil {
		return "", fmt.Errorf("attachment_describe: %w", err)
	}
	if rec == nil {
		return "", fmt.Errorf("attachment not found: %s", id)
	}

	if !strings.HasPrefix(rec.ContentType, "image/") {
		return fmt.Sprintf("Attachment %s is %s, not an image — vision analysis is not applicable.", id, rec.ContentType), nil
	}

	if t.analyzer == nil {
		if rec.Description != "" {
			return rec.Description, nil
		}
		return "Vision analysis is not configured. Enable attachments.vision in config to analyze images.", nil
	}

	reanalyze, _ := args["reanalyze"].(bool)
	modelOverride, _ := args["model"].(string)
	promptOverride, _ := args["prompt"].(string)

	if reanalyze || modelOverride != "" || promptOverride != "" {
		// Build a temporary analyzer if prompt override is needed.
		analyzer := t.analyzer
		if promptOverride != "" {
			analyzer = NewAnalyzer(t.store, AnalyzerConfig{
				Client:  t.analyzer.client,
				Model:   t.analyzer.model,
				Prompt:  promptOverride,
				Timeout: t.analyzer.timeout,
				Logger:  t.analyzer.logger,
			})
		}

		desc, err := analyzer.Reanalyze(ctx, rec, modelOverride)
		if err != nil {
			return "", fmt.Errorf("attachment_describe: reanalyze: %w", err)
		}
		if desc == "" {
			return "Vision model returned an empty description.", nil
		}
		return desc, nil
	}

	// Standard analyze — uses cache when available.
	desc, err := t.analyzer.Analyze(ctx, rec)
	if err != nil {
		return "", fmt.Errorf("attachment_describe: %w", err)
	}
	if desc == "" {
		return "Vision model returned an empty description.", nil
	}
	return desc, nil
}

// Search performs a text search across attachment metadata and returns
// matching records.
func (t *Tools) Search(ctx context.Context, args map[string]any) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	limit := 10
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}

	records, err := t.store.Search(ctx, SearchParams{
		Query: query,
		Limit: limit,
	})
	if err != nil {
		return "", fmt.Errorf("attachment_search: %w", err)
	}
	if len(records) == 0 {
		return "No attachments found matching the query.", nil
	}

	summaries := make([]attachmentSummary, len(records))
	for i, rec := range records {
		summaries[i] = summarizeRecord(rec)
	}

	data, err := json.Marshal(summaries)
	if err != nil {
		return "", fmt.Errorf("attachment_search: marshal: %w", err)
	}
	return string(data), nil
}
