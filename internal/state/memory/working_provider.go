package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// WorkingMemoryProvider implements [agent.TagContextProvider] for
// auto-injecting working memory into the system prompt. When the
// current conversation has working memory content, it is included
// under a "### Working Memory" heading so the agent has experiential
// continuity without needing to explicitly read it. Registered via
// [agent.Loop.RegisterAlwaysContextProvider].
type WorkingMemoryProvider struct {
	store            *WorkingMemoryStore
	conversationFunc func(context.Context) string
}

// NewWorkingMemoryProvider creates a context provider that auto-injects
// working memory for the current conversation. The convFunc parameter
// extracts the conversation ID from the request context — typically
// [tools.ConversationIDFromContext].
func NewWorkingMemoryProvider(store *WorkingMemoryStore, convFunc func(context.Context) string) *WorkingMemoryProvider {
	return &WorkingMemoryProvider{
		store:            store,
		conversationFunc: convFunc,
	}
}

// TagContext returns the working memory content for the current
// conversation, formatted for system prompt injection. Returns empty
// string if no working memory exists. Implements
// [agent.TagContextProvider]; registered via
// RegisterAlwaysContextProvider.
func (p *WorkingMemoryProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	convID := p.conversationFunc(ctx)

	content, updatedAt, err := p.store.Get(convID)
	if err != nil {
		return "", fmt.Errorf("read working memory: %w", err)
	}
	if content == "" {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("### Working Memory\n\n")
	if !updatedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("*Last updated: %s*\n\n", promptfmt.FormatDeltaOnly(updatedAt, time.Now())))
	}
	sb.WriteString(content)

	return sb.String(), nil
}
