package memory

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// MessageChannelProviderConfig configures the conversation-timing and
// older-sessions context provider for message-channel conversations
// (Signal, Matrix, iMessage). Zero values fall back to defaults.
type MessageChannelProviderConfig struct {
	// RecentWindow excludes sessions that ended within this duration
	// of now. Their messages are still present in the model's
	// role-native working message list, so cataloging them would
	// restate context the model already sees. The same window is the
	// conversation-timing narrative's active/resumed boundary, so the
	// two views describe one span. Default: 30m.
	RecentWindow time.Duration

	// SessionsLimit caps the number of session entries listed in the
	// catalog block. Default: 5.
	SessionsLimit int

	// SessionsByteCap is the JSON output ceiling for the catalog.
	// Default: 8000.
	SessionsByteCap int

	// Timezone is the household IANA timezone anchoring the timing
	// narrative's day words ("last night", "yesterday"). Empty falls
	// back to the system's local zone.
	Timezone string

	// OriginFromContext extracts the current turn's message provenance
	// (the Origin* constants) from a request context — pass
	// tools.MessageOriginFromContext. Telling a counterparty-contact
	// turn from an internal wake is load-bearing for the timing
	// narrative, so when this is nil the narrative is omitted entirely
	// rather than rendered on a guess.
	OriginFromContext func(context.Context) string

	// HintsFromContext extracts the request routing hints — pass
	// tools.HintsFromContext. Optional: on wake turns the loop_name
	// hint names the waking loop in the narrative.
	HintsFromContext func(context.Context) map[string]string
}

// MessageChannelProvider injects two context blocks for message-channel
// conversations.
//
// "Conversation Timing" is one or two sentences of Go-side interpreted
// narrative — where the current turn stands in the rhythm of contact
// with the channel counterparty ("You're in an active conversation…",
// "This is the first contact today. The previous conversation was last
// night, about…"). Rendering is [promptfmt.ConversationRecency]; the
// facts come from contact-classified message timestamps
// ([ArchiveStore.RecentContactTimes]), the active session, and the most
// recent archivist-summarized closed session. Rhythm anchors on contact
// timestamps, never session boundaries — wake rows keep sessions open,
// so session duration is only rendered inside the contact-gated active
// branch.
//
// "Older Sessions" is a compact JSON catalog of recent closed sessions
// with substance, acting as enticement to call
// archive_session_transcript or archive_search for fuller content. The
// catalog keeps the precise deltas and session ids; the narrative is an
// interpretation layer over the same timestamps, not a second copy of
// the data.
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

// TagContextBucket places the timing narrative and older-sessions
// catalog in continuity context: both orient the current turn inside
// past experience, and the bucket is already uncached so the ticking
// deltas cost nothing.
func (p *MessageChannelProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketContinuity
}

// TagContext returns the conversation-timing narrative and the
// older-sessions catalog for the active conversation. Returns the empty
// string when there is nothing to emit (no conversation context, and
// neither block has content).
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

	timing := p.conversationTiming(ctx, convID, now)

	older, more, err := p.olderSessions(convID, cutoff)
	if err != nil {
		p.logger.Warn("older sessions query failed",
			"conversation_id", convID, "error", err)
	}
	if timing == "" && len(older) == 0 {
		return "", nil
	}

	var sb strings.Builder
	if timing != "" {
		sb.WriteString("## Conversation Timing\n\n")
		sb.WriteString(timing)
		sb.WriteString("\n")
	}
	if len(older) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		// The truncated flag covers both fitting passes — the entry
		// limit and the byte cap — so dropped sessions are never
		// silent.
		sessionsJSON := FitPrefix(len(older), p.cfg.SessionsByteCap, func(k int) []byte {
			return FormatSessionsList(older[:k], now, more || k < len(older))
		})
		sb.WriteString("## Older Sessions\n\n")
		sb.WriteString("Past sessions on this conversation, newest first. Use archive_session_transcript for full transcripts, or archive_search to search semantically.\n\n")
		sb.WriteString("```json\n")
		sb.Write(sessionsJSON)
		sb.WriteString("\n```\n")
	}
	return sb.String(), nil
}

// conversationTiming assembles the facts for the timing narrative and
// renders it, or returns "" when the narrative cannot be stated
// truthfully: no origin extractor is wired (contact vs wake would be a
// guess), or the queries that anchor it fail. Failures degrade to the
// catalog-only output rather than erroring the whole context bucket.
func (p *MessageChannelProvider) conversationTiming(ctx context.Context, convID string, now time.Time) string {
	if p.cfg.OriginFromContext == nil {
		return ""
	}

	facts := promptfmt.ConversationRecencyFacts{
		Kind:         promptfmt.TurnContact,
		RecentWindow: p.cfg.RecentWindow,
	}
	if p.cfg.OriginFromContext(ctx) == OriginWake {
		facts.Kind = promptfmt.TurnWake
		if p.cfg.HintsFromContext != nil {
			facts.WakeLoopName = p.cfg.HintsFromContext(ctx)["loop_name"]
		}
	}

	contacts, err := p.archive.RecentContactTimes(convID, 2)
	if err != nil {
		p.logger.Warn("recent contact times query failed",
			"conversation_id", convID, "error", err)
		return ""
	}
	// On a contact turn the newest contact row is the current message
	// itself (stored before prompt assembly), so the previous contact
	// is the second entry — and the current message's stored arrival
	// time becomes the anchor the gap is measured to, per the contract
	// that the gap ends at arrival, not at prompt assembly. Assembly
	// can lag arrival, and an anchor drifting across the recent-window
	// or a day boundary would select the wrong rhythm bucket. A wake
	// stores no contact row, so its previous contact is the first entry
	// and wall-clock now is the honest anchor.
	anchor := now
	idx := 0
	if facts.Kind == promptfmt.TurnContact {
		idx = 1
		if len(contacts) > 0 {
			anchor = contacts[0]
		}
	}
	if len(contacts) > idx {
		facts.PreviousContactAt = contacts[idx]
		facts.HasHistory = true
	}

	if sess, err := p.archive.ActiveSession(convID); err != nil {
		// Degrading to the session-less wording is fine; doing it
		// silently on a real query failure is not — no rows is (nil,
		// nil), so this only fires on genuine breakage.
		p.logger.Warn("active session lookup failed",
			"conversation_id", convID, "error", err)
	} else if sess != nil {
		facts.ActiveSessionStart = sess.StartedAt
	}

	if sessions, err := p.archive.ListSessions(convID, previousSessionScan); err != nil {
		p.logger.Warn("previous session lookup failed",
			"conversation_id", convID, "error", err)
	} else {
		for _, s := range sessions {
			if s.EndedAt == nil || !sessionHasContent(s) {
				continue
			}
			facts.PreviousSessionEnd = *s.EndedAt
			facts.PreviousSessionTopic = previousSessionTopic(s)
			facts.HasHistory = true
			break
		}
	}

	return promptfmt.ConversationRecency(facts, anchor, p.loadLocation())
}

// previousSessionScan bounds how many recent sessions the timing
// narrative examines to find the most recent closed one with content.
const previousSessionScan = 10

// previousSessionTopic picks the clause-sized summary of a session:
// the archivist's one-liner first, then the title. Empty when the
// archivist has not summarized yet — the clause degrades to the time
// word alone.
func previousSessionTopic(s *Session) string {
	if s.Metadata != nil && s.Metadata.OneLiner != "" {
		return s.Metadata.OneLiner
	}
	return s.Title
}

// loadLocation returns the configured household timezone, falling back
// to the system's local zone.
func (p *MessageChannelProvider) loadLocation() *time.Location {
	if p.cfg.Timezone != "" {
		if loc, err := time.LoadLocation(p.cfg.Timezone); err == nil {
			return loc
		}
	}
	return time.Now().Location()
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
