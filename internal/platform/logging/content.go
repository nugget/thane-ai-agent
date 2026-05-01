package logging

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// ContentWriter writes request-level content (system prompts, tool
// call details, message bodies) to the log index database. It is
// safe for concurrent use.
type ContentWriter struct {
	db                *sql.DB
	maxLen            int // 0 = unlimited
	logger            *slog.Logger
	stmtUpsertPrompt  *sql.Stmt
	stmtInsertRequest *sql.Stmt
	stmtDeleteTools   *sql.Stmt
	stmtInsertTool    *sql.Stmt
}

// NewContentWriter creates a writer for the given logs.db connection.
// maxLen controls the maximum character count for retained content
// fields (tool results, message bodies). Pass 0 for unlimited.
func NewContentWriter(db *sql.DB, maxLen int, logger *slog.Logger) (*ContentWriter, error) {
	upsertPrompt, err := db.Prepare(`INSERT INTO log_prompts (hash, content, first_seen)
		VALUES (?, ?, ?)
		ON CONFLICT(hash) DO NOTHING`)
	if err != nil {
		return nil, fmt.Errorf("prepare upsert prompt: %w", err)
	}

	insertRequest, err := db.Prepare(`INSERT OR REPLACE INTO log_request_content
		(request_id, prompt_hash, user_content, assistant_content, model,
		 iteration_count, input_tokens, output_tokens, tools_used, messages_json,
		 exhausted, exhaust_reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		upsertPrompt.Close()
		return nil, fmt.Errorf("prepare insert request: %w", err)
	}

	deleteTools, err := db.Prepare(`DELETE FROM log_tool_content WHERE request_id = ?`)
	if err != nil {
		upsertPrompt.Close()
		insertRequest.Close()
		return nil, fmt.Errorf("prepare delete tools: %w", err)
	}

	insertTool, err := db.Prepare(`INSERT INTO log_tool_content
		(request_id, iteration_index, tool_call_id, tool_name, arguments, result, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		upsertPrompt.Close()
		insertRequest.Close()
		deleteTools.Close()
		return nil, fmt.Errorf("prepare insert tool: %w", err)
	}

	return &ContentWriter{
		db:                db,
		maxLen:            maxLen,
		logger:            logger,
		stmtUpsertPrompt:  upsertPrompt,
		stmtInsertRequest: insertRequest,
		stmtDeleteTools:   deleteTools,
		stmtInsertTool:    insertTool,
	}, nil
}

// Close releases prepared statements.
func (w *ContentWriter) Close() error {
	w.stmtUpsertPrompt.Close()
	w.stmtInsertRequest.Close()
	w.stmtDeleteTools.Close()
	w.stmtInsertTool.Close()
	return nil
}

// RequestContent holds the data to persist for a completed request.
type RequestContent struct {
	RequestID    string
	SystemPrompt string // full assembled system prompt
	UserContent  string // inbound user message
	Model        string

	// From iterate.Result:
	AssistantContent string
	IterationCount   int
	InputTokens      int
	OutputTokens     int
	ToolsUsed        map[string]int
	Exhausted        bool
	ExhaustReason    string

	// Full message history sent to the model, retained for forensics and
	// tool-call extraction. Image bytes are omitted from retained detail.
	Messages []llm.Message
}

// WriteRequest persists a completed request's content. The system
// prompt is stored content-addressed (deduplicated by SHA-256 hash).
// Tool call arguments and results are extracted from the message
// history. Errors are logged but not returned — content retention is
// best-effort and must never block request processing.
func (w *ContentWriter) WriteRequest(ctx context.Context, rc RequestContent) {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Store system prompt (deduplicated by hash).
	promptHash := hashPrompt(rc.SystemPrompt)
	if _, err := w.stmtUpsertPrompt.ExecContext(ctx, promptHash, rc.SystemPrompt, now); err != nil {
		w.logger.Warn("content retention: failed to store prompt",
			"request_id", rc.RequestID,
			"error", err,
		)
	}

	// Marshal tools_used map as JSON.
	var toolsUsedJSON string
	if len(rc.ToolsUsed) > 0 {
		b, _ := json.Marshal(rc.ToolsUsed)
		toolsUsedJSON = string(b)
	}
	messagesJSON, err := marshalRetainedMessages(rc.Messages, w.maxLen)
	if err != nil {
		w.logger.Warn("content retention: failed to marshal retained messages",
			"request_id", rc.RequestID,
			"error", err,
		)
	}

	// Store request-level content.
	if _, err := w.stmtInsertRequest.ExecContext(ctx,
		rc.RequestID,
		promptHash,
		w.truncate(rc.UserContent),
		w.truncate(rc.AssistantContent),
		rc.Model,
		rc.IterationCount,
		rc.InputTokens,
		rc.OutputTokens,
		nullStr(toolsUsedJSON),
		nullStr(messagesJSON),
		rc.Exhausted,
		nullStr(rc.ExhaustReason),
		now,
	); err != nil {
		w.logger.Warn("content retention: failed to store request content",
			"request_id", rc.RequestID,
			"error", err,
		)
	}

	// Extract and store tool call content from the message history.
	w.writeToolCalls(ctx, rc.RequestID, rc.Messages, now)
}

// writeToolCalls walks the message history and persists tool call
// arguments and results. Tool calls appear as assistant messages with
// ToolCalls, followed by tool-role messages with the result content.
func (w *ContentWriter) writeToolCalls(ctx context.Context, requestID string, messages []llm.Message, now string) {
	// Delete any existing tool rows for this request to prevent
	// duplicates if WriteRequest is called more than once (e.g.,
	// INSERT OR REPLACE on the request row doesn't cascade).
	if _, err := w.stmtDeleteTools.ExecContext(ctx, requestID); err != nil {
		w.logger.Warn("content retention: failed to clear old tool rows",
			"request_id", requestID,
			"error", err,
		)
	}

	// Build a map of tool_call_id → result content from tool messages.
	results := make(map[string]string)
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			results[m.ToolCallID] = m.Content
		}
	}

	// Walk assistant messages for tool calls. Track iteration index by
	// counting assistant turns (each assistant message ≈ one iteration).
	iterIdx := 0
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		if len(m.ToolCalls) == 0 {
			// Text-only assistant turn — still counts as an iteration if
			// it's not the system message.
			iterIdx++
			continue
		}

		for _, tc := range m.ToolCalls {
			argsJSON, _ := json.Marshal(tc.Function.Arguments)
			result := results[tc.ID]

			if _, err := w.stmtInsertTool.ExecContext(ctx,
				requestID,
				iterIdx,
				nullStr(tc.ID),
				tc.Function.Name,
				nullStr(w.truncate(string(argsJSON))),
				nullStr(w.truncate(result)),
				now,
			); err != nil {
				w.logger.Warn("content retention: failed to store tool call",
					"request_id", requestID,
					"tool", tc.Function.Name,
					"error", err,
				)
			}
		}
		iterIdx++
	}
}

// truncate limits s to w.maxLen runes without allocating a []rune
// slice. When maxLen is 0 or s is within the limit, the string is
// returned unchanged. Uses a for-range walk to find the byte offset
// of the maxLen-th rune boundary.
func (w *ContentWriter) truncate(s string) string {
	if w.maxLen <= 0 {
		return s
	}
	runeCount := 0
	for i := range s {
		if runeCount == w.maxLen {
			return s[:i]
		}
		runeCount++
	}
	return s
}

// hashPrompt returns the hex-encoded SHA-256 hash of a system prompt.
func hashPrompt(prompt string) string {
	h := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(h[:])
}

// nullStr returns a sql.NullString that is null when s is empty.
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
