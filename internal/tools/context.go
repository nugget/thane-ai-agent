package tools

import "context"

type contextKey string

const conversationIDKey contextKey = "conversation_id"

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
