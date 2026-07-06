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

// olderSessionsMaxScan bounds how many candidate rows one turn will
// examine while filling the catalog. A conversation would need this
// many consecutive zero-message sessions before the cutoff to starve
// the catalog; when the ceiling is hit the result is marked as having
// more, so the drop is never silent.
const olderSessionsMaxScan = 500

// olderSessions returns closed, non-empty sessions on the given
// conversation that ended before the cutoff, most recently ended
// first, capped at SessionsLimit. Active and in-window sessions are
// excluded in SQL because their messages are still in the model's
// working message list; zero-message sessions are filtered here as
// noise (message counts can live on a separate DB connection, so the
// filter cannot be pushed into the session query). The second return
// reports whether qualifying sessions beyond the limit are known or
// possible, so the catalog can mark itself truncated.
func (p *MessageChannelProvider) olderSessions(conversationID string, cutoff time.Time) ([]*Session, bool, error) {
	pageSize := p.cfg.SessionsLimit * 4
	if pageSize < 20 {
		pageSize = 20
	}
	out := make([]*Session, 0, p.cfg.SessionsLimit)
	for offset := 0; offset < olderSessionsMaxScan; offset += pageSize {
		page, err := p.archive.ListClosedSessionsEndedBefore(conversationID, cutoff, pageSize, offset)
		if err != nil {
			return out, false, err
		}
		for _, s := range page {
			if s.MessageCount == 0 {
				continue
			}
			if len(out) >= p.cfg.SessionsLimit {
				// One qualifying session beyond the limit proves
				// the catalog is a window.
				return out, true, nil
			}
			out = append(out, s)
		}
		if len(page) < pageSize {
			return out, false, nil
		}
	}
	return out, true, nil
}
