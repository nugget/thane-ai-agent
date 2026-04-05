package tools

import (
	"context"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
)

type contextKey string

const conversationIDKey contextKey = "conversation_id"
const sessionIDKey contextKey = "session_id"
const toolCallIDKey contextKey = "tool_call_id"
const iterationIndexKey contextKey = "iteration_index"
const hintsKey contextKey = "hints"
const loopIDKey contextKey = "loop_id"

// WithConversationID adds the conversation ID to the context.
func WithConversationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, conversationIDKey, id)
}

// ConversationIDFromContext extracts the conversation ID from the context.
// Returns "default" if not set.
func ConversationIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(conversationIDKey).(string); ok && id != "" {
		return id
	}
	return "default"
}

// WithSessionID adds the archive session ID to the context. This allows
// downstream code (e.g. delegate executor) to discover its parent session.
func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionIDKey, id)
}

// SessionIDFromContext extracts the archive session ID from the context.
// Returns an empty string if not set.
func SessionIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(sessionIDKey).(string); ok {
		return id
	}
	return ""
}

// WithToolCallID adds the tool call ID to the context. This allows
// downstream code (e.g. delegate executor) to discover which tool call
// triggered it.
func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDKey, id)
}

// ToolCallIDFromContext extracts the tool call ID from the context.
// Returns an empty string if not set.
func ToolCallIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(toolCallIDKey).(string); ok {
		return id
	}
	return ""
}

// WithIterationIndex adds the current loop iteration index to the context.
func WithIterationIndex(ctx context.Context, idx int) context.Context {
	return context.WithValue(ctx, iterationIndexKey, idx)
}

// IterationIndexFromContext extracts the loop iteration index from the
// context. Returns -1 and false if not set.
func IterationIndexFromContext(ctx context.Context) (int, bool) {
	if idx, ok := ctx.Value(iterationIndexKey).(int); ok {
		return idx, true
	}
	return -1, false
}

// WithLoopID adds the calling loop's ID to the context. This allows
// tool handlers (e.g. delegate executor) to discover which loop
// invoked them for parent-child relationship tracking.
func WithLoopID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, loopIDKey, id)
}

// LoopIDFromContext extracts the calling loop's ID from the context.
// Returns an empty string if not set (e.g. non-loop API requests).
func LoopIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(loopIDKey).(string); ok {
		return id
	}
	return ""
}

// WithHints adds routing hints to the context. Nil hints are ignored
// (the original context is returned unchanged).
func WithHints(ctx context.Context, hints map[string]string) context.Context {
	if hints == nil {
		return ctx
	}
	return context.WithValue(ctx, hintsKey, hints)
}

// HintsFromContext extracts routing hints from the context. Returns nil
// if no hints were set.
func HintsFromContext(ctx context.Context) map[string]string {
	if h, ok := ctx.Value(hintsKey).(map[string]string); ok {
		return h
	}
	return nil
}

// LoopCompletionTargetFromContext derives the most natural detached
// completion target for the current tool call context. The returned
// conversation ID always reflects the current live conversation when one
// is available, even when the preferred detached delivery target is a
// channel target such as Signal or OWU.
func LoopCompletionTargetFromContext(ctx context.Context) (looppkg.Completion, string, *looppkg.CompletionChannelTarget) {
	conversationID := strings.TrimSpace(ConversationIDFromContext(ctx))
	hints := HintsFromContext(ctx)
	source := strings.TrimSpace(hints["source"])
	sender := strings.TrimSpace(hints["sender"])
	switch {
	case source == "signal" && sender != "":
		return looppkg.CompletionChannel, conversationID, &looppkg.CompletionChannelTarget{
			Channel:        "signal",
			Recipient:      sender,
			ConversationID: conversationID,
		}
	case source == "owu" || strings.HasPrefix(conversationID, "owu-"):
		return looppkg.CompletionChannel, conversationID, &looppkg.CompletionChannelTarget{
			Channel:        "owu",
			ConversationID: conversationID,
		}
	default:
		return looppkg.CompletionConversation, conversationID, nil
	}
}
