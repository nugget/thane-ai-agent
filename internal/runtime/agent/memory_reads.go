package agent

import (
	"context"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/logging"
)

// A telemetry failure must not discard the router's outcome or resource
// cooldown. The already-built prompt provides an explicit estimate fallback.
func (l *Loop) routerContextTokens(ctx context.Context, conversationID string, estimate int) int {
	tokens, err := l.memory.GetTokenCount(ctx, conversationID)
	if err != nil {
		logging.Logger(ctx).Warn("failed to read router context tokens; using prompt estimate",
			"error", err, "estimated_context_tokens", estimate)
		return estimate
	}
	return tokens
}

func (l *Loop) maybeCompact(ctx context.Context, conversationID string) {
	if l.compactor == nil {
		return
	}
	needed, err := l.compactor.NeedsCompaction(ctx, conversationID)
	if err != nil {
		logging.Logger(ctx).Warn("failed to check conversation compaction", "error", err)
		return
	}
	if !needed {
		return
	}
	go func() {
		// Compaction may outlive the completed turn, but its model call and
		// all surrounding reads share a bounded lifetime and log metadata.
		compactCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		log := logging.Logger(compactCtx)
		started := time.Now()
		preTokens, tokenErr := l.memory.GetTokenCount(compactCtx, conversationID)
		preMessages, messageErr := l.memory.GetMessages(compactCtx, conversationID)
		before := []any{}
		if tokenErr != nil {
			log.Warn("failed to read context tokens before compaction", "error", tokenErr)
		} else {
			before = append(before, "tokens_before", preTokens)
		}
		if messageErr != nil {
			log.Warn("failed to read messages before compaction", "error", messageErr)
		} else {
			before = append(before, "messages_before", len(preMessages))
		}
		log.Info("triggering compaction", before...)
		if err := l.compactor.Compact(compactCtx, conversationID); err != nil {
			log.Error("compaction failed", "error", err)
			return
		}
		after := []any{"elapsed", time.Since(started).Round(time.Second)}
		if postTokens, err := l.memory.GetTokenCount(compactCtx, conversationID); err != nil {
			log.Warn("failed to read context tokens after compaction", "error", err)
		} else {
			after = append(after, "tokens_after", postTokens)
			if tokenErr == nil {
				after = append(after, "tokens_freed", preTokens-postTokens)
			}
		}
		if postMessages, err := l.memory.GetMessages(compactCtx, conversationID); err != nil {
			log.Warn("failed to read messages after compaction", "error", err)
		} else {
			after = append(after, "messages_after", len(postMessages))
			if messageErr == nil {
				after = append(after, "messages_compacted", len(preMessages)-len(postMessages))
			}
		}
		log.Info("compaction completed", after...)
	}()
}
