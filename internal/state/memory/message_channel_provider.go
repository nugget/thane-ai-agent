package memory

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// MessageChannelProviderConfig configures the older-sessions catalog
// provider for message-channel conversations (Signal, Matrix, iMessage).
// Zero values fall back to defaults.
type MessageChannelProviderConfig struct {
	// RecentWindow excludes sessions that ended within this duration
	// of now. Their messages are still present in the model's
	// role-native working message list, so cataloging them would
	// restate context the model already sees. Default: 30m.
	RecentWindow time.Duration

	// SessionsLimit caps the number of session entries listed in the
	// catalog block. Default: 5.
	SessionsLimit int

	// SessionsByteCap is the JSON output ceiling for the catalog.
	// Default: 8000.
	SessionsByteCap int
}

// MessageChannelProvider injects an "Older Sessions" context block for
// message-channel conversations: a compact JSON catalog of recent
// closed sessions with substance, acting as enticement to call
// archive_session_transcript or archive_search for fuller content.
//
// Verbatim conversation history is deliberately NOT emitted here. The
// model's working message list already carries stored history in
// role-native messages; a second in-prompt transcript was the largest
// duplicated-context source found by the #1160 audit and broke the
// prompt-cache prefix every turn. Sessions whose messages are still in
// the working list (ended within [RecentWindow], or still active) are
// excluded so the catalog never restates visible context, and sessions
// with zero messages are skipped as noise.
//
// Implements [agent.TagContextProvider] via structural typing; gated
// on the message_channel capability tag asserted by Signal (and future
// Matrix/iMessage) inbound bridges.
type MessageChannelProvider struct {
	archive               *ArchiveStore
	conversationIDFromCtx func(context.Context) string
	cfg                   MessageChannelProviderConfig
	logger                *slog.Logger
	nowFunc               func() time.Time
}

// NewMessageChannelProvider creates the provider. The
// conversationIDFromCtx function extracts the active conversation ID
// from a request context — pass [tools.ConversationIDFromContext].
// Zero-valued config fields fall back to defaults documented on
// [MessageChannelProviderConfig].
func NewMessageChannelProvider(archive *ArchiveStore, conversationIDFromCtx func(context.Context) string, cfg MessageChannelProviderConfig, logger *slog.Logger) *MessageChannelProvider {
	if cfg.RecentWindow <= 0 {
		cfg.RecentWindow = 30 * time.Minute
	}
	if cfg.SessionsLimit <= 0 {
		cfg.SessionsLimit = 5
	}
	if cfg.SessionsByteCap <= 0 {
		cfg.SessionsByteCap = 8000
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &MessageChannelProvider{
		archive:               archive,
		conversationIDFromCtx: conversationIDFromCtx,
		cfg:                   cfg,
		logger:                logger,
		nowFunc:               time.Now,
	}
}

// TagContextBucket places the older-sessions catalog in continuity
// context.
func (p *MessageChannelProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketContinuity
}

// TagContext returns the older-sessions catalog for the active
// conversation. Returns the empty string when there is nothing to emit
// (no conversation context, no cataloged sessions).
func (p *MessageChannelProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	if p.conversationIDFromCtx == nil {
		return "", nil
	}
	convID := p.conversationIDFromCtx(ctx)
	if convID == "" || convID == "default" {
		return "", nil
	}

	now := p.nowFunc()
	cutoff := now.Add(-p.cfg.RecentWindow)

	older, more, err := p.olderSessions(convID, cutoff)
	if err != nil {
		p.logger.Warn("older sessions query failed",
			"conversation_id", convID, "error", err)
	}
	if len(older) == 0 {
		return "", nil
	}

	// The truncated flag covers both fitting passes — the entry limit
	// and the byte cap — so dropped sessions are never silent.
	sessionsJSON := FitPrefix(len(older), p.cfg.SessionsByteCap, func(k int) []byte {
		return FormatSessionsList(older[:k], now, more || k < len(older))
	})

	var sb strings.Builder
	sb.WriteString("## Older Sessions\n\n")
	sb.WriteString("Past sessions on this conversation, newest first. Use archive_session_transcript for full transcripts, or archive_search to search semantically.\n\n")
	sb.WriteString("```json\n")
	sb.Write(sessionsJSON)
	sb.WriteString("\n```\n")
	return sb.String(), nil
}

// olderSessions returns closed, non-empty sessions on the given
// conversation that ended before the cutoff, newest first, capped at
// SessionsLimit. Active sessions and sessions ending after the cutoff
// are excluded because their messages are still in the model's working
// message list; zero-message sessions are excluded as noise. The
// second return reports whether qualifying sessions were dropped by
// the limit, so the catalog can mark itself truncated.
func (p *MessageChannelProvider) olderSessions(conversationID string, cutoff time.Time) ([]*Session, bool, error) {
	// Over-fetch so the cutoff and empty-session filters still have
	// enough candidates to fill the limit. Empty sessions are common
	// on quiet conversations (session rotation without traffic), so
	// the buffer is generous. When even the over-fetch is exhausted,
	// deeper qualifying sessions can go uncounted — acceptable, since
	// the truncated flag is already set well before that point.
	sessions, err := p.archive.ListSessions(conversationID, p.cfg.SessionsLimit*8)
	if err != nil {
		return nil, false, err
	}
	out := make([]*Session, 0, p.cfg.SessionsLimit)
	qualifying := 0
	for _, s := range sessions {
		if s.EndedAt == nil {
			continue
		}
		if !s.EndedAt.Before(cutoff) {
			continue
		}
		if s.MessageCount == 0 {
			continue
		}
		qualifying++
		if len(out) < p.cfg.SessionsLimit {
			out = append(out, s)
		}
	}
	return out, qualifying > len(out), nil
}
