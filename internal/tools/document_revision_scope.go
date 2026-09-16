package tools

import (
	"context"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// DocumentRevisionScope identifies the model consciousness whose document
// reads should protect later writes. Runtime-scoped document adapters use the
// same scope so generated and global writers share one receipt/CAS path. A
// loop/conversation pair is stable across turns without leaking receipts
// between concurrent conversations; request is only a last-resort scope for
// one-shot callers.
//
// For a service loop the conversation is the wake — every wake mints its
// own — so the scope is per wake, and a receipt must be recorded inside the
// wake that consumes it. The loop ID and conversation come from this
// package's own context keys when a tool call set them, and otherwise from
// the loop runtime's keys, which are stamped earlier on the same wake: the
// output-context render that shows a loop its own documents runs on that
// earlier context, and the receipt it records has to resolve to the very
// scope the generated output tool computes moments later.
func DocumentRevisionScope(ctx context.Context) string {
	parts := make([]string, 0, 2)
	loopID := strings.TrimSpace(LoopIDFromContext(ctx))
	if loopID == "" {
		loopID = strings.TrimSpace(looppkg.LoopIDFromContext(ctx))
	}
	if loopID != "" {
		parts = append(parts, "loop:"+loopID)
	}
	conversationID := strings.TrimSpace(ConversationIDFromContext(ctx))
	if conversationID == "" || conversationID == "default" {
		conversationID = strings.TrimSpace(looppkg.ConversationIDFromContext(ctx))
	}
	if conversationID != "" && conversationID != "default" {
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

func documentRevisionScope(ctx context.Context) string {
	return DocumentRevisionScope(ctx)
}
