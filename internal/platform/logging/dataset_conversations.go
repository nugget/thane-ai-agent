package logging

import (
	"context"
	"log/slog"
	"time"
)

// NewConversationsRecorder returns a [RequestRecordFunc] that emits each
// completed LLM request as a [DatasetRecord] in the conversations
// dataset (sources/thane/conversations/...). The record carries the
// request envelope — model, system prompt, user/assistant content,
// token counts, tool usage — keyed by request_id. Full message-history
// fidelity stays in the sqlite content path until the interactions
// normalizer in #938 takes over.
//
// Returns nil when writer is nil so the caller can compose with
// [CombineRequestRecorders] without a per-site nil check.
func NewConversationsRecorder(writer *DatasetWriter, logger *slog.Logger) RequestRecordFunc {
	if writer == nil {
		return nil
	}
	return func(_ context.Context, rc RequestContent) {
		record := DatasetRecordFromRequestContent(rc, time.Now())
		if err := writer.WriteRecord(record); err != nil && logger != nil {
			logger.Warn("failed to write conversations dataset record",
				"request_id", rc.RequestID,
				"error", err,
			)
		}
	}
}

// DatasetRecordFromRequestContent converts a completed LLM request into a
// conversations dataset record. Content fields are written verbatim —
// the conversations dataset is a pristine source of LLM req/resp, so
// [LoggingConfig.ContentMaxLength] does not apply here. Sinks with a
// retention budget (ContentWriter, live_requests) truncate locally on
// their own write path. The full Messages slice is intentionally not
// embedded — message-level fidelity belongs in the interactions corpus
// (#938); this record captures the request envelope and links back via
// RequestID.
func DatasetRecordFromRequestContent(rc RequestContent, now time.Time) DatasetRecord {
	payload := map[string]any{
		"model":             rc.Model,
		"system_prompt":     rc.SystemPrompt,
		"user_content":      rc.UserContent,
		"assistant_content": rc.AssistantContent,
		"iteration_count":   rc.IterationCount,
		"input_tokens":      rc.InputTokens,
		"output_tokens":     rc.OutputTokens,
		"exhausted":         rc.Exhausted,
		"message_count":     len(rc.Messages),
	}
	if rc.ExhaustReason != "" {
		payload["exhaust_reason"] = rc.ExhaustReason
	}
	if len(rc.ToolsUsed) > 0 {
		payload["tools_used"] = rc.ToolsUsed
	}
	return DatasetRecord{
		Timestamp:     now.UTC(),
		Dataset:       DatasetConversations,
		Kind:          "request_complete",
		SchemaVersion: 1,
		RequestID:     rc.RequestID,
		Source:        "agent",
		Severity:      "INFO",
		Payload:       payload,
	}
}
