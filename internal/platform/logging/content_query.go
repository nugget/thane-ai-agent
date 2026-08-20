package logging

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RequestDetail holds the full content retained for a single request,
// ready for JSON serialization by the web API.
type RequestDetail struct {
	RequestID        string            `json:"request_id"`
	PromptHash       string            `json:"prompt_hash,omitempty"`
	SystemPrompt     string            `json:"system_prompt,omitempty"`
	Messages         []MessageDetail   `json:"messages"`
	UserContent      string            `json:"user_content,omitempty"`
	AssistantContent string            `json:"assistant_content,omitempty"`
	Model            string            `json:"model,omitempty"`
	IterationCount   int               `json:"iteration_count"`
	InputTokens      int               `json:"input_tokens"`
	OutputTokens     int               `json:"output_tokens"`
	ToolsUsed        map[string]int    `json:"tools_used,omitempty"`
	Exhausted        bool              `json:"exhausted"`
	ExhaustReason    string            `json:"exhaust_reason,omitempty"`
	CreatedAt        string            `json:"created_at"`
	ToolCalls        []ToolDetail      `json:"tool_calls"`
	ModelCalls       []ModelCallDetail `json:"model_calls,omitempty"`
}

// ToolDetail holds the retained content for a single tool invocation.
type ToolDetail struct {
	ToolCallID     string `json:"tool_call_id,omitempty"`
	ToolName       string `json:"tool_name"`
	Arguments      string `json:"arguments,omitempty"`
	Result         string `json:"result,omitempty"`
	IterationIndex int    `json:"iteration_index"`
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
	ID                 string `json:"id,omitempty"`
	Name               string `json:"name"`
	Arguments          string `json:"arguments,omitempty"`
	ArgumentsTruncated bool   `json:"arguments_truncated,omitempty"`
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

// QueryRecentModelCallRequestIDs returns newest-first retained request IDs that
// contain replayable per-iteration model-call capture. A non-empty model
// filters on the deployment recorded for any captured call. Limit must be positive.
func QueryRecentModelCallRequestIDs(ctx context.Context, db *sql.DB, since time.Time, model string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("model-call request limit must be positive")
	}
	query := `SELECT request_id FROM log_request_content
		WHERE model_calls_json IS NOT NULL
		  AND model_calls_json <> ''
		  AND model_calls_json <> '[]'
		  AND created_at >= ?`
	args := []any{since.UTC().Format(time.RFC3339Nano)}
	if model = strings.TrimSpace(model); model != "" {
		query += ` AND json_valid(model_calls_json)
			AND EXISTS (
				SELECT 1 FROM json_each(model_calls_json) AS model_call
				WHERE json_extract(model_call.value, '$.model') = ?
			)`
		args = append(args, model)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query retained model-call requests: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan retained model-call request: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate retained model-call requests: %w", err)
	}
	return ids, nil
}

// queryRequestDetailCtx is the context-aware implementation shared by
// QueryRequestDetail and the Archiver.
func queryRequestDetailCtx(ctx context.Context, db *sql.DB, requestID string) (*RequestDetail, error) {
	var (
		rd                             RequestDetail
		promptHash, userContent        sql.NullString
		assistantContent, model        sql.NullString
		messagesJSON, modelCallsJSON   sql.NullString
		toolsUsed                      sql.NullString
		exhaustReason                  sql.NullString
		iterCount, inputTok, outputTok sql.NullInt64
		exhausted                      sql.NullBool
		createdAt                      string
	)

	err := db.QueryRowContext(ctx, `SELECT request_id, prompt_hash, user_content, assistant_content,
		model, iteration_count, input_tokens, output_tokens, tools_used, messages_json, model_calls_json,
		exhausted, exhaust_reason, created_at
		FROM log_request_content WHERE request_id = ?`, requestID).Scan(
		&rd.RequestID, &promptHash, &userContent, &assistantContent,
		&model, &iterCount, &inputTok, &outputTok, &toolsUsed,
		&messagesJSON, &modelCallsJSON, &exhausted, &exhaustReason, &createdAt,
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
	if modelCallsJSON.Valid && modelCallsJSON.String != "" {
		if err := json.Unmarshal([]byte(modelCallsJSON.String), &rd.ModelCalls); err != nil {
			return &rd, fmt.Errorf("decode retained model calls: %w", err)
		}
		if err := resolveModelCallPrompts(ctx, db, rd.ModelCalls); err != nil {
			return &rd, err
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
	rows, err := db.QueryContext(ctx, `SELECT tool_call_id, tool_name, arguments, result, iteration_index
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
		if err := rows.Scan(&callID, &td.ToolName, &args, &result, &td.IterationIndex); err != nil {
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
