package memory

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// MessageChannelProviderConfig configures the verbatim-tail + older-
// sessions context provider for message-channel conversations (Signal,
// Matrix, iMessage). Zero values fall back to defaults.
type MessageChannelProviderConfig struct {
	// TailWindow is the time bound on the verbatim tail. Default: 30m.
	TailWindow time.Duration

	// TailMinMessages is the floor: at least this many of the most
	// recent archived messages on the conversation are returned even
	// if the time window is empty. Default: 50.
	TailMinMessages int

	// TailMaxMessages caps the verbatim tail before byte-cap fitting.
	// Default: 200.
	TailMaxMessages int

	// TailByteCap is the JSON output ceiling for the verbatim tail.
	// Default: 32000.
	TailByteCap int

	// OlderSessionsLimit caps the number of older-session entries
	// listed in the catalog block. Default: 20.
	OlderSessionsLimit int

	// OlderSessionsByteCap is the JSON output ceiling for the older-
	// sessions catalog. Default: 16000.
	OlderSessionsByteCap int
}

// MessageChannelProvider injects two context blocks into the system
// prompt for message-channel conversations:
//
//   - "Recent Conversation" — verbatim tail of recent archived messages
//     (last [TailWindow] OR floor of [TailMinMessages] of the most
//     recent, whichever yields more, capped at [TailMaxMessages] and
//     [TailByteCap]). Crosses session boundaries. Excludes the active
//     session's currently in-memory rows so the model does not see
//     them twice (once in its working message list, again here).
//
//   - "Older Sessions" — JSON metadata block for sessions ending
//     before the verbatim window. Acts as enticement to call
//     archive_session_transcript or archive_search for fuller content.
//
// Implements [agent.TagContextProvider] via structural typing; gated
// on the message_channel capability tag asserted by Signal (and future
// Matrix/iMessage) inbound bridges.
//
// Output sits in the system prompt's DYNAMIC CONTEXT section per
// [docs/anthropic-caching.md]: the delta timestamps tick every turn so
// the block is intrinsically uncached, but the cached prefix above it
// stays warm.
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
	if cfg.TailWindow <= 0 {
		cfg.TailWindow = 30 * time.Minute
	}
	if cfg.TailMinMessages <= 0 {
		cfg.TailMinMessages = 50
	}
	if cfg.TailMaxMessages <= 0 {
		cfg.TailMaxMessages = 200
	}
	if cfg.TailByteCap <= 0 {
		cfg.TailByteCap = 32000
	}
	if cfg.OlderSessionsLimit <= 0 {
		cfg.OlderSessionsLimit = 20
	}
	if cfg.OlderSessionsByteCap <= 0 {
		cfg.OlderSessionsByteCap = 16000
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

// TagContext returns the verbatim tail + older-sessions blocks for the
// active conversation. Returns the empty string when there is nothing
// to emit (no conversation context, no archived content).
func (p *MessageChannelProvider) TagContext(ctx context.Context) (string, error) {
	if p.conversationIDFromCtx == nil {
		return "", nil
	}
	convID := p.conversationIDFromCtx(ctx)
	if convID == "" || convID == "default" {
		return "", nil
	}

	now := p.nowFunc()
	windowStart := now.Add(-p.cfg.TailWindow)

	// Resolve the active session so we can drop its rows from the
	// verbatim tail — those messages are already in the model's
	// working message list.
	var excludeSessionID string
	if active, err := p.archive.ActiveSession(convID); err == nil && active != nil {
		excludeSessionID = active.ID
	}

	var sb strings.Builder

	// ---- Verbatim tail ----
	messages, msgsTruncated, err := p.archive.GetMessagesInRange(RangeOptions{
		ConversationID:   convID,
		ExcludeSessionID: excludeSessionID,
		From:             windowStart,
		To:               now,
		MinMessages:      p.cfg.TailMinMessages,
		MaxMessages:      p.cfg.TailMaxMessages,
	})
	if err != nil {
		p.logger.Warn("verbatim tail query failed",
			"conversation_id", convID, "error", err)
	}
	if len(messages) > 0 {
		tailJSON := FitSuffix(len(messages), p.cfg.TailByteCap, func(drop int) []byte {
			return FormatRecentMessages(messages[drop:], now, msgsTruncated || drop > 0)
		})
		sb.WriteString("## Recent Conversation\n\n")
		sb.WriteString("Verbatim history. Crosses session boundaries; sessions are an internal abstraction here.\n\n")
		sb.WriteString("```json\n")
		sb.Write(tailJSON)
		sb.WriteString("\n```\n")
	}

	// ---- Older sessions ----
	older, err := p.olderSessions(convID, windowStart)
	if err != nil {
		p.logger.Warn("older sessions query failed",
			"conversation_id", convID, "error", err)
	}
	if len(older) > 0 {
		sessionsJSON := FitPrefix(len(older), p.cfg.OlderSessionsByteCap, func(k int) []byte {
			return FormatSessionsList(older[:k], now, k < len(older))
		})
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("## Older Sessions\n\n")
		sb.WriteString("Sessions before the tail above. Use archive_session_transcript for full transcripts, or archive_search to search semantically.\n\n")
		sb.WriteString("```json\n")
		sb.Write(sessionsJSON)
		sb.WriteString("\n```\n")
	}

	return sb.String(), nil
}

// olderSessions returns closed sessions on the given conversation that
// ended before the verbatim window started, capped at OlderSessionsLimit.
// Active sessions and sessions still inside the verbatim window are
// excluded so the two blocks don't duplicate content.
func (p *MessageChannelProvider) olderSessions(conversationID string, windowStart time.Time) ([]*Session, error) {
	// Pull a small over-fetch so the windowStart filter still has
	// enough candidates for the limit when recent sessions are
	// excluded.
	sessions, err := p.archive.ListSessions(conversationID, p.cfg.OlderSessionsLimit*2)
	if err != nil {
		return nil, err
	}
	out := make([]*Session, 0, p.cfg.OlderSessionsLimit)
	for _, s := range sessions {
		if s.EndedAt == nil {
			continue
		}
		if !s.EndedAt.Before(windowStart) {
			continue
		}
		out = append(out, s)
		if len(out) >= p.cfg.OlderSessionsLimit {
			break
		}
	}
	return out, nil
}
