package agent

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// Auxiliary work inherits the request's identity but resolves the current
// session at its own boundary: a lifecycle tool can rotate sessions mid-turn.
func (l *Loop) withUsageSession(ctx context.Context, conversationID string) context.Context {
	attribution := llm.AttributionFromContext(ctx)
	attribution.ConversationID = conversationID
	attribution.SessionID = ""
	if l.archiver != nil {
		attribution.SessionID = l.archiver.ActiveSessionID(conversationID)
	}
	return llm.WithAttribution(ctx, attribution)
}
