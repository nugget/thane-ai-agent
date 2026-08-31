package tools

import (
	"context"
	"strings"
)

// documentRevisionScope identifies the model consciousness whose document
// reads should protect later writes. A loop/conversation pair is stable across
// turns without leaking receipts between concurrent conversations; request is
// only a last-resort scope for one-shot callers.
func documentRevisionScope(ctx context.Context) string {
	parts := make([]string, 0, 2)
	if loopID := strings.TrimSpace(LoopIDFromContext(ctx)); loopID != "" {
		parts = append(parts, "loop:"+loopID)
	}
	if conversationID := strings.TrimSpace(ConversationIDFromContext(ctx)); conversationID != "" && conversationID != "default" {
		parts = append(parts, "conversation:"+conversationID)
	}
	if len(parts) > 0 {
		return strings.Join(parts, "/")
	}
	if sessionID := strings.TrimSpace(SessionIDFromContext(ctx)); sessionID != "" {
		return "session:" + sessionID
	}
	if requestID := strings.TrimSpace(RequestIDFromContext(ctx)); requestID != "" {
		return "request:" + requestID
	}
	return ""
}
