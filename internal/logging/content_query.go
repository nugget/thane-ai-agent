package logging

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// RequestDetail holds the full content retained for a single request,
// ready for JSON serialization by the web API.
type RequestDetail struct {
	RequestID        string         `json:"request_id"`
	PromptHash       string         `json:"prompt_hash,omitempty"`
	SystemPrompt     string         `json:"system_prompt,omitempty"`
	UserContent      string         `json:"user_content,omitempty"`
	AssistantContent string         `json:"assistant_content,omitempty"`
	Model            string         `json:"model,omitempty"`
	IterationCount   int            `json:"iteration_count"`
	InputTokens      int            `json:"input_tokens"`
	OutputTokens     int            `json:"output_tokens"`
	ToolsUsed        map[string]int `json:"tools_used,omitempty"`
	Exhausted        bool           `json:"exhausted"`
	ExhaustReason    string         `json:"exhaust_reason,omitempty"`
	CreatedAt        string         `json:"created_at"`
	ToolCalls        []ToolDetail   `json:"tool_calls"`
}

// ToolDetail holds the retained content for a single tool invocation.
type ToolDetail struct {
	ToolCallID     string `json:"tool_call_id,omitempty"`
	ToolName       string `json:"tool_name"`
	Arguments      string `json:"arguments,omitempty"`
	Result         string `json:"result,omitempty"`
	IterationIndex int    `json:"iteration_index"`
}

// QueryRequestDetail fetches the full retained content for a request by
// its request ID. Returns nil, nil if the request is not found or if
// content retention was not active when the request was processed.
func QueryRequestDetail(db *sql.DB, requestID string) (*RequestDetail, error) {
	var (
		rd                             RequestDetail
		promptHash, userContent        sql.NullString
		assistantContent, model        sql.NullString
		iterCount, inputTok, outputTok sql.NullInt64
		toolsUsed, exhaustReason       sql.NullString
		exhausted                      sql.NullBool
		createdAt                      string
	)

	err := db.QueryRow(`SELECT request_id, prompt_hash, user_content, assistant_content,
		model, iteration_count, input_tokens, output_tokens, tools_used,
		exhausted, exhaust_reason, created_at
		FROM log_request_content WHERE request_id = ?`, requestID).Scan(
		&rd.RequestID, &promptHash, &userContent, &assistantContent,
		&model, &iterCount, &inputTok, &outputTok, &toolsUsed,
		&exhausted, &exhaustReason, &createdAt,
	)
	if err == sql.ErrNoRows {
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

	// Resolve system prompt content from the prompts table.
	if rd.PromptHash != "" {
		var content sql.NullString
		_ = db.QueryRow(`SELECT content FROM log_prompts WHERE hash = ?`, rd.PromptHash).Scan(&content)
		rd.SystemPrompt = content.String
	}

	// Fetch tool calls for this request.
	rows, err := db.Query(`SELECT tool_call_id, tool_name, arguments, result, iteration_index
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
