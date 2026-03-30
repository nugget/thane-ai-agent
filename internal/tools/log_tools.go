package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/awareness"
	"github.com/nugget/thane-ai-agent/internal/logging"
)

// SetLogIndexDB adds the logs_query tool to the registry so the agent
// can query its own structured log index for self-diagnostics. If db
// is nil (log indexing disabled), the tool is not registered.
func (r *Registry) SetLogIndexDB(db *sql.DB) {
	r.logIndexDB = db
	r.registerLogsQuery()
}

// registerLogsQuery registers the logs_query tool.
func (r *Registry) registerLogsQuery() {
	if r.logIndexDB == nil {
		return
	}

	r.Register(&Tool{
		Name: "logs_query",
		Description: "Query the structured log index for debugging and forensics. " +
			"Filter by session, conversation, request ID, subsystem, tool, model, " +
			"log level (minimum severity), time range, and message pattern. " +
			"Returns matching entries as JSON.",
		AlwaysAvailable: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{
					"type":        "string",
					"description": "Filter by session ID.",
				},
				"conversation_id": map[string]any{
					"type":        "string",
					"description": "Filter by conversation ID.",
				},
				"request_id": map[string]any{
					"type":        "string",
					"description": "Filter by request ID (correlates one user-to-response cycle).",
				},
				"subsystem": map[string]any{
					"type":        "string",
					"enum":        []string{"agent", "delegate", "metacog", "scheduler", "api"},
					"description": "Filter by subsystem.",
				},
				"tool": map[string]any{
					"type":        "string",
					"description": "Filter by tool name.",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Filter by LLM model name.",
				},
				"loop_id": map[string]any{
					"type":        "string",
					"description": "Filter by loop ID (UUID). Matches entries from a specific loop instance.",
				},
				"loop_name": map[string]any{
					"type":        "string",
					"description": "Filter by loop name (e.g., \"metacognitive\", \"signal-parent\", \"email-poller\").",
				},
				"level": map[string]any{
					"type":        "string",
					"enum":        []string{"ERROR", "WARN", "INFO", "DEBUG"},
					"description": "Minimum log level (default: INFO). Use DEBUG only when you need low-level tracing — it produces very large result sets.",
				},
				"since": map[string]any{
					"type":        "string",
					"description": "Start of time range. ISO 8601 timestamp or relative duration (e.g., \"1h\", \"30m\", \"24h\", \"7d\").",
				},
				"until": map[string]any{
					"type":        "string",
					"description": "End of time range. ISO 8601 timestamp. Defaults to now.",
				},
				"pattern": map[string]any{
					"type":        "string",
					"description": "Text search in log message (substring match).",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum entries to return (default 50, max 200).",
				},
			},
		},
		Handler: r.handleLogsQuery,
	})
}

// maxLogsResultBytes caps the serialized JSON response to prevent
// context bombs when queries match many entries. The model can always
// narrow filters and query again.
const maxLogsResultBytes = 50 * 1024

// handleLogsQuery implements the logs_query tool.
func (r *Registry) handleLogsQuery(_ context.Context, args map[string]any) (string, error) {
	params := logging.QueryParams{
		SessionID:      stringArg(args, "session_id"),
		ConversationID: stringArg(args, "conversation_id"),
		RequestID:      stringArg(args, "request_id"),
		Subsystem:      stringArg(args, "subsystem"),
		Tool:           stringArg(args, "tool"),
		Model:          stringArg(args, "model"),
		LoopID:         stringArg(args, "loop_id"),
		LoopName:       stringArg(args, "loop_name"),
		Level:          stringArg(args, "level"),
		Pattern:        stringArg(args, "pattern"),
	}

	// Default level to INFO — DEBUG is extremely noisy and rarely
	// useful for analysis. The model can explicitly request DEBUG
	// when needed.
	if params.Level == "" {
		params.Level = "INFO"
	}

	// Handle limit from JSON (float64 or int).
	switch v := args["limit"].(type) {
	case float64:
		params.Limit = int(v)
	case int:
		params.Limit = v
	}

	if s := stringArg(args, "since"); s != "" {
		params.Since = parseTimeOrDuration(s)
	}
	if s := stringArg(args, "until"); s != "" {
		params.Until = parseTimestamp(s)
	}

	entries, err := logging.Query(r.logIndexDB, params)
	if err != nil {
		return "", fmt.Errorf("logs_query: %w", err)
	}

	// Build compact JSON response (one line per entry, not pretty-printed).
	type jsonEntry struct {
		Timestamp      string         `json:"ts"`
		Level          string         `json:"level"`
		Msg            string         `json:"msg"`
		RequestID      string         `json:"request_id,omitempty"`
		SessionID      string         `json:"session_id,omitempty"`
		ConversationID string         `json:"conversation_id,omitempty"`
		Subsystem      string         `json:"subsystem,omitempty"`
		Tool           string         `json:"tool,omitempty"`
		Model          string         `json:"model,omitempty"`
		LoopID         string         `json:"loop_id,omitempty"`
		LoopName       string         `json:"loop_name,omitempty"`
		Attrs          map[string]any `json:"attrs,omitempty"`
		Source         string         `json:"source,omitempty"`
	}

	// Marshal entries one at a time with a byte budget. Stop when
	// we'd exceed the safety cap. Timestamps use delta format per #458.
	//
	// "returned" is the number of rows the DB gave us (after LIMIT).
	// "count" is how many we actually included before the byte cap.
	// When count < returned, the response is truncated.
	now := time.Now()
	returned := len(entries)

	// Reserve space for the JSON envelope and potential truncation note.
	const envelopeOverhead = 256
	byteBudget := maxLogsResultBytes - envelopeOverhead

	var marshaledEntries [][]byte
	included := 0
	bytesSoFar := 0
	truncated := false

	for _, e := range entries {
		je := jsonEntry{
			Timestamp:      awareness.FormatDeltaOnly(e.Timestamp, now),
			Level:          e.Level,
			Msg:            e.Msg,
			RequestID:      e.RequestID,
			SessionID:      e.SessionID,
			ConversationID: e.ConversationID,
			Subsystem:      e.Subsystem,
			Tool:           e.Tool,
			Model:          e.Model,
			LoopID:         e.LoopID,
			LoopName:       e.LoopName,
		}
		if e.SourceFile != "" {
			je.Source = fmt.Sprintf("%s:%d", e.SourceFile, e.SourceLine)
		}
		if e.Attrs != "" {
			var attrs map[string]any
			if json.Unmarshal([]byte(e.Attrs), &attrs) == nil {
				je.Attrs = attrs
			}
		}

		entryJSON, err := json.Marshal(je)
		if err != nil {
			continue
		}

		// Would this entry push us over the byte budget?
		entrySize := len(entryJSON) + 1 // +1 for comma separator
		if bytesSoFar+entrySize > byteBudget {
			truncated = true
			break
		}

		marshaledEntries = append(marshaledEntries, entryJSON)
		bytesSoFar += entrySize
		included++
	}

	// Build final JSON.
	var sb strings.Builder
	fmt.Fprintf(&sb, `{"count":%d,"returned":%d,"entries":[`, included, returned)
	for i, entry := range marshaledEntries {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.Write(entry)
	}
	sb.WriteString("]")
	if truncated {
		fmt.Fprintf(&sb, `,"truncated":true,"note":"showing %d of %d returned entries (byte limit). Narrow filters for more targeted results."`, included, returned)
	}
	sb.WriteString("}")

	return sb.String(), nil
}

// stringArg extracts a string value from the args map.
func stringArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

// parseTimeOrDuration interprets s as either a relative duration
// (subtracted from now) or an ISO 8601 timestamp. Returns zero time
// on failure, which the Query function treats as "no filter".
func parseTimeOrDuration(s string) time.Time {
	// Try Go duration first ("1h", "30m", "24h").
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d)
	}

	// Try day suffix ("7d", "30d") — not supported by time.ParseDuration.
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		}
	}

	// Fall back to ISO 8601 timestamp.
	return parseTimestamp(s)
}

// parseTimestamp tries to parse s as an ISO 8601 timestamp in both
// RFC3339 and RFC3339Nano formats. Returns zero time on failure.
func parseTimestamp(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Time{}
}
