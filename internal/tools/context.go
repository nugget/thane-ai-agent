package tools

import "context"

type contextKey string

const conversationIDKey contextKey = "conversation_id"
const hintsKey contextKey = "hints"

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
