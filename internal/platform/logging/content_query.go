package logging

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	maxRetainedToolDefinitionSnapshots = 10
	maxRetainedToolDefinitionsPerSnap  = 128
	defaultRetainedToolDefinitionLen   = 4096
)

// RequestDetail holds the full content retained for a single request,
// ready for JSON serialization by the web API.
type RequestDetail struct {
	RequestID        string          `json:"request_id"`
	PromptHash       string          `json:"prompt_hash,omitempty"`
	SystemPrompt     string          `json:"system_prompt,omitempty"`
	Messages         []MessageDetail `json:"messages"`
	UserContent      string          `json:"user_content,omitempty"`
	AssistantContent string          `json:"assistant_content,omitempty"`
	Model            string          `json:"model,omitempty"`
	IterationCount   int             `json:"iteration_count"`
	InputTokens      int             `json:"input_tokens"`
	OutputTokens     int             `json:"output_tokens"`
	ToolsUsed        map[string]int  `json:"tools_used,omitempty"`
	Exhausted        bool            `json:"exhausted"`
	ExhaustReason    string          `json:"exhaust_reason,omitempty"`
	CreatedAt        string          `json:"created_at"`
	ToolCalls        []ToolDetail    `json:"tool_calls"`
	ToolDefinitions  []ToolDefDetail `json:"tool_definitions,omitempty"`
}

// ToolDetail holds the retained content for a single tool invocation.
type ToolDetail struct {
	ToolCallID     string `json:"tool_call_id,omitempty"`
	ToolName       string `json:"tool_name"`
	Arguments      string `json:"arguments,omitempty"`
	Result         string `json:"result,omitempty"`
	IterationIndex int    `json:"iteration_index"`
	CreatedAt      string `json:"created_at,omitempty"`
}

// ToolDefDetail holds the model-facing tool definition list offered to a
// single model iteration.
type ToolDefDetail struct {
	IterationIndex   int              `json:"iteration_index"`
	Tools            []map[string]any `json:"tools"`
	ToolsTruncated   bool             `json:"tools_truncated,omitempty"`
	ContentTruncated bool             `json:"content_truncated,omitempty"`
}

// NewToolDefDetail returns a defensive copy of tool definitions for one
// model iteration.
func NewToolDefDetail(iterationIndex int, tools []map[string]any) ToolDefDetail {
	return ToolDefDetail{
		IterationIndex: iterationIndex,
		Tools:          cloneToolDefMaps(tools),
	}
}

// CloneToolDefDetails returns a defensive copy of retained tool-definition
// snapshots.
func CloneToolDefDetails(src []ToolDefDetail) []ToolDefDetail {
	if len(src) == 0 {
		return nil
	}
	out := make([]ToolDefDetail, len(src))
	for i, snap := range src {
		out[i] = ToolDefDetail{
			IterationIndex:   snap.IterationIndex,
			Tools:            cloneToolDefMaps(snap.Tools),
			ToolsTruncated:   snap.ToolsTruncated,
			ContentTruncated: snap.ContentTruncated,
		}
	}
	return out
}

func retainedToolDefDetails(src []ToolDefDetail, maxLen int) []ToolDefDetail {
	if len(src) == 0 {
		return nil
	}
	snapshotCount := len(src)
	if snapshotCount > maxRetainedToolDefinitionSnapshots {
		snapshotCount = maxRetainedToolDefinitionSnapshots
	}
	out := make([]ToolDefDetail, 0, snapshotCount)
	for i := 0; i < snapshotCount; i++ {
		snap := src[i]
		toolCount := len(snap.Tools)
		toolsTruncated := snap.ToolsTruncated
		if toolCount > maxRetainedToolDefinitionsPerSnap {
			toolCount = maxRetainedToolDefinitionsPerSnap
			toolsTruncated = true
		}
		tools, contentTruncated := retainedToolDefMaps(snap.Tools[:toolCount], maxLen)
		out = append(out, ToolDefDetail{
			IterationIndex:   snap.IterationIndex,
			Tools:            tools,
			ToolsTruncated:   toolsTruncated,
			ContentTruncated: snap.ContentTruncated || contentTruncated,
		})
	}
	return out
}

func retainedToolDefMaps(src []map[string]any, maxLen int) ([]map[string]any, bool) {
	if len(src) == 0 {
		return nil, false
	}
	out := make([]map[string]any, len(src))
	var truncated bool
	for i, item := range src {
		out[i], truncated = retainedToolDefMap(item, maxLen, truncated)
	}
	return out, truncated
}

func retainedToolDefMap(src map[string]any, maxLen int, truncated bool) (map[string]any, bool) {
	if src == nil {
		return nil, truncated
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key], truncated = retainedToolDefValue(value, maxLen, truncated)
	}
	return out, truncated
}

func retainedToolDefValue(value any, maxLen int, truncated bool) (any, bool) {
	switch v := value.(type) {
	case string:
		out, cut := truncateRetainedContentWithFlag(v, toolDefinitionStringLimit(maxLen))
		return out, truncated || cut
	case map[string]any:
		return retainedToolDefMap(v, maxLen, truncated)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i], truncated = retainedToolDefValue(item, maxLen, truncated)
		}
		return out, truncated
	case []map[string]any:
		out, cut := retainedToolDefMaps(v, maxLen)
		return out, truncated || cut
	case []string:
		out := make([]string, len(v))
		for i, item := range v {
			var cut bool
			out[i], cut = truncateRetainedContentWithFlag(item, toolDefinitionStringLimit(maxLen))
			truncated = truncated || cut
		}
		return out, truncated
	default:
		return value, truncated
	}
}

func toolDefinitionStringLimit(maxLen int) int {
	if maxLen > 0 {
		return maxLen
	}
	return defaultRetainedToolDefinitionLen
}

func cloneToolDefMaps(src []map[string]any) []map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make([]map[string]any, len(src))
	for i, item := range src {
		out[i] = cloneToolDefMap(item)
	}
	return out
}

func cloneToolDefMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = cloneToolDefValue(value)
	}
	return out
}

func cloneToolDefValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneToolDefMap(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = cloneToolDefValue(item)
		}
		return out
	case []map[string]any:
		return cloneToolDefMaps(v)
	case []string:
		return append([]string(nil), v...)
	default:
		return value
	}
}

// MessageDetail holds one retained chat message from the provider-neutral
// message payload sent to the model. Image bytes are intentionally omitted;
// only media metadata is retained for forensics.
type MessageDetail struct {
	Index            int                     `json:"index"`
	Role             string                  `json:"role"`
	Content          string                  `json:"content,omitempty"`
	ContentTruncated bool                    `json:"content_truncated,omitempty"`
	ToolCalls        []MessageToolCallDetail `json:"tool_calls,omitempty"`
	ToolCallID       string                  `json:"tool_call_id,omitempty"`
	Images           []MessageImageDetail    `json:"images,omitempty"`
}

// MessageToolCallDetail describes a tool call embedded in an assistant
// message from the retained chat payload.
type MessageToolCallDetail struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

// MessageImageDetail describes a retained image attachment without storing
// the base64 image payload itself.
type MessageImageDetail struct {
	MediaType string `json:"media_type,omitempty"`
}

// QueryRequestDetail fetches the full retained content for a request by
// its request ID. Returns nil, nil if the request is not found or if
// content retention was not active when the request was processed.
func QueryRequestDetail(db *sql.DB, requestID string) (*RequestDetail, error) {
	return queryRequestDetailCtx(context.Background(), db, requestID)
}

// queryRequestDetailCtx is the context-aware implementation shared by
// QueryRequestDetail and the Archiver.
func queryRequestDetailCtx(ctx context.Context, db *sql.DB, requestID string) (*RequestDetail, error) {
	var (
		rd                             RequestDetail
		promptHash, userContent        sql.NullString
		assistantContent, model        sql.NullString
		messagesJSON, toolDefsJSON     sql.NullString
		toolsUsed                      sql.NullString
		exhaustReason                  sql.NullString
		iterCount, inputTok, outputTok sql.NullInt64
		exhausted                      sql.NullBool
		createdAt                      string
	)

	err := db.QueryRowContext(ctx, `SELECT request_id, prompt_hash, user_content, assistant_content,
		model, iteration_count, input_tokens, output_tokens, tools_used, messages_json,
		tool_definitions_json, exhausted, exhaust_reason, created_at
		FROM log_request_content WHERE request_id = ?`, requestID).Scan(
		&rd.RequestID, &promptHash, &userContent, &assistantContent,
		&model, &iterCount, &inputTok, &outputTok, &toolsUsed,
		&messagesJSON, &toolDefsJSON, &exhausted, &exhaustReason, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query request content: %w", err)
	}

	rd.PromptHash = promptHash.String
	rd.UserContent = userContent.String
	rd.AssistantContent = assistantContent.String
	rd.Model = model.String
	rd.IterationCount = int(iterCount.Int64)
	rd.InputTokens = int(inputTok.Int64)
	rd.OutputTokens = int(outputTok.Int64)
	rd.Exhausted = exhausted.Bool
	rd.ExhaustReason = exhaustReason.String
	rd.CreatedAt = createdAt

	if toolsUsed.Valid && toolsUsed.String != "" {
		var tu map[string]int
		if err := json.Unmarshal([]byte(toolsUsed.String), &tu); err == nil {
			rd.ToolsUsed = tu
		}
	}
	if messagesJSON.Valid && messagesJSON.String != "" {
		if err := json.Unmarshal([]byte(messagesJSON.String), &rd.Messages); err != nil {
			return &rd, fmt.Errorf("decode retained messages: %w", err)
		}
	}
	if rd.Messages == nil {
		rd.Messages = []MessageDetail{}
	}
	if toolDefsJSON.Valid && toolDefsJSON.String != "" {
		if err := json.Unmarshal([]byte(toolDefsJSON.String), &rd.ToolDefinitions); err != nil {
			return &rd, fmt.Errorf("decode retained tool definitions: %w", err)
		}
	}

	// Resolve system prompt content from the prompts table.
	if rd.PromptHash != "" {
		var content sql.NullString
		err := db.QueryRowContext(ctx, `SELECT content FROM log_prompts WHERE hash = ?`, rd.PromptHash).Scan(&content)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return &rd, fmt.Errorf("query prompt content: %w", err)
		}
		rd.SystemPrompt = content.String
	}

	// Fetch tool calls for this request.
	rows, err := db.QueryContext(ctx, `SELECT tool_call_id, tool_name, arguments, result, iteration_index, created_at
		FROM log_tool_content WHERE request_id = ? ORDER BY iteration_index, id`, requestID)
	if err != nil {
		return &rd, fmt.Errorf("query tool content: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			td                   ToolDetail
			callID, args, result sql.NullString
		)
		if err := rows.Scan(&callID, &td.ToolName, &args, &result, &td.IterationIndex, &td.CreatedAt); err != nil {
			return &rd, fmt.Errorf("scan tool row: %w", err)
		}
		td.ToolCallID = callID.String
		td.Arguments = args.String
		td.Result = result.String
		rd.ToolCalls = append(rd.ToolCalls, td)
	}

	if rd.ToolCalls == nil {
		rd.ToolCalls = []ToolDetail{}
	}

	return &rd, rows.Err()
}
