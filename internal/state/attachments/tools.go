package attachments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// maxToolResultBytes caps JSON tool output to prevent context flooding.
// Matches the convention used by archive_search (~16KB ≈ 4K tokens).
const maxToolResultBytes = 16000

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
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	ContentType   string `json:"content_type"`
	Size          int64  `json:"size"`
	Channel       string `json:"channel,omitempty"`
	Sender        string `json:"sender,omitempty"`
	ReceivedDelta string `json:"received_delta"`
	Description   string `json:"description,omitempty"`
}

func summarizeRecord(rec *Record, now time.Time) attachmentSummary {
	desc := rec.Description
	// Truncate long descriptions in list view on rune boundaries
	// to avoid splitting multi-byte UTF-8 characters.
	runes := []rune(desc)
	if len(runes) > 200 {
		desc = string(runes[:197]) + "..."
	}
	return attachmentSummary{
		ID:            rec.ID,
		Name:          rec.OriginalName,
		ContentType:   rec.ContentType,
		Size:          rec.Size,
		Channel:       rec.Channel,
		Sender:        rec.Sender,
		ReceivedDelta: promptfmt.FormatDeltaOnly(rec.ReceivedAt, now),
		Description:   desc,
	}
}

// truncateResultUTF8 truncates s to at most maxBytes, ensuring the
// result is valid UTF-8, and appends a truncation notice.
func truncateResultUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	b := maxBytes
	for b > 0 && !utf8.RuneStart(s[b]) {
		b--
	}
	return s[:b] + fmt.Sprintf(
		"\n\n[Truncated: %d bytes total, showing first %d bytes. Use attachment_describe for full details on a specific attachment.]",
		len(s), b,
	)
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

	now := time.Now()
	summaries := make([]attachmentSummary, len(records))
	for i, rec := range records {
		summaries[i] = summarizeRecord(rec, now)
	}

	data, err := json.Marshal(summaries)
	if err != nil {
		return "", fmt.Errorf("attachment_list: marshal: %w", err)
	}
	return truncateResultUTF8(string(data), maxToolResultBytes), nil
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
		return fmt.Sprintf("Attachment %s has no description, and vision analysis is not enabled here, so it cannot be analyzed. You do not know what this image shows.", id), nil
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
			return "", describeFailure(rec, fmt.Errorf("attachment_describe: reanalyze: %w", err))
		}
		return desc, nil
	}

	// Standard analyze — uses cache when available.
	desc, err := t.analyzer.Analyze(ctx, rec)
	if err != nil {
		return "", describeFailure(rec, fmt.Errorf("attachment_describe: %w", err))
	}
	return desc, nil
}

// describeFailure attaches to a failed analysis the text the model
// reads in place of the error's own, which carries the stored file's
// path and the provider's raw response: why there is no new
// description, whether an earlier one still stands, and whether calling
// again can help.
func describeFailure(rec *Record, err error) error {
	var text string
	if rec.Description != "" {
		// Only a reanalysis fails on a record that has a description.
		text = fmt.Sprintf("Reanalysis of attachment %s failed: %s. Its earlier description still stands; attachment_describe without reanalyze, model or prompt returns it.", rec.ID, VisionFailureReason(err))
	} else {
		text = fmt.Sprintf("Attachment %s was not analyzed: %s. You do not know what this image shows.", rec.ID, VisionFailureReason(err))
	}
	return &promptfmt.ModelFacingError{Text: text + describeNextMove(err), Err: err}
}

// describeNextMove says how calling again can help, or nothing when the
// failure gives no reason to think it would.
func describeNextMove(err error) string {
	var ambiguous *llm.AmbiguousModelError
	switch {
	case errors.As(err, &ambiguous):
		targets, _ := json.Marshal(ambiguous.Targets)
		return fmt.Sprintf(" Call again with the model argument set to one of %s.", targets)
	case errors.Is(err, ErrNoDescription):
		return " Calling again may produce a description."
	case errors.Is(err, ErrVisionCall):
		failure := llm.ClassifyCallFailure(err)
		if failure.Persistent() {
			return " Calling again with the same model will fail the same way; the model argument can name a different image-capable model."
		}
		if failure.Retryable() {
			return " Calling again may succeed."
		}
	}
	return ""
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

	now := time.Now()
	summaries := make([]attachmentSummary, len(records))
	for i, rec := range records {
		summaries[i] = summarizeRecord(rec, now)
	}

	data, err := json.Marshal(summaries)
	if err != nil {
		return "", fmt.Errorf("attachment_search: marshal: %w", err)
	}
	return truncateResultUTF8(string(data), maxToolResultBytes), nil
}
