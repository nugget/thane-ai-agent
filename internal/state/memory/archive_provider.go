package memory

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
)

// ArchiveSearcher abstracts archive search for testing. ArchiveStore
// satisfies this interface — no adapter needed.
type ArchiveSearcher interface {
	Search(opts SearchOptions) ([]SearchResult, error)
}

// ArchiveContextProvider implements [agent.TagContextProvider] for
// injecting relevant past conversation excerpts into the system
// prompt. This is Layer 2 of the pre-warming system: Layer 1 provides
// knowledge (facts + KB docs), Layer 2 provides experiential judgment
// (prior reasoning about similar situations). Registered via
// [agent.Loop.RegisterAlwaysContextProvider].
type ArchiveContextProvider struct {
	store      ArchiveSearcher
	maxResults int
	maxBytes   int
	logger     *slog.Logger
}

// NewArchiveContextProvider creates a context provider that searches the
// conversation archive for relevant past exchanges. maxResults caps the
// number of search hits; maxBytes caps the formatted output size to
// prevent context flooding.
func NewArchiveContextProvider(store ArchiveSearcher, maxResults, maxBytes int, logger *slog.Logger) *ArchiveContextProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &ArchiveContextProvider{
		store:      store,
		maxResults: maxResults,
		maxBytes:   maxBytes,
		logger:     logger,
	}
}

// TagContextBucket places archive prewarm hits in related context
// because they are retrieval results selected for the current wake or
// request rather than baseline continuity.
func (p *ArchiveContextProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketRelated
}

// TagContext searches the conversation archive for excerpts relevant
// to the current wake context. Subjects are extracted from ctx (set
// by the wake bridge); if no subjects are available but
// req.UserMessage is short, it falls back to searching by message
// content. Implements [agent.TagContextProvider]; registered via
// RegisterAlwaysContextProvider.
//
// Returns empty string when there is nothing to search for or no
// results are found. Errors from the archive store are logged and
// swallowed — archive injection should never block a wake.
func (p *ArchiveContextProvider) TagContext(ctx context.Context, req agentctx.ContextRequest) (string, error) {
	subjects := knowledge.SubjectsFromContext(ctx)
	userMessage := req.UserMessage

	query, querySource := p.buildQuery(subjects, userMessage)
	if query == "" {
		p.logger.Debug("archive pre-warm: no searchable query",
			"subjects_count", len(subjects),
			"message_len", len(userMessage),
		)
		return "", nil
	}

	p.logger.Debug("archive pre-warm: query constructed",
		"subjects", subjects,
		"query", query,
		"source", querySource,
	)

	start := time.Now()
	results, err := p.store.Search(SearchOptions{
		Query: query,
		Limit: p.maxResults,
	})
	elapsed := time.Since(start)

	if err != nil {
		p.logger.Warn("archive pre-warm search failed",
			"query", query,
			"error", err,
			"took_ms", elapsed.Milliseconds(),
		)
		return "", nil
	}

	p.logger.Debug("archive pre-warm: search completed",
		"query", query,
		"results", len(results),
		"took_ms", elapsed.Milliseconds(),
	)

	if len(results) == 0 {
		return "", nil
	}

	for i, r := range results {
		p.logger.Debug("archive pre-warm: result",
			"index", i,
			"session_id", r.SessionID,
			"match_role", r.Match.Role,
			"match_preview", previewContent(r.Match.Content),
			"context_before", len(r.ContextBefore),
			"context_after", len(r.ContextAfter),
		)
	}

	return p.formatResults(results), nil
}

// maxUserMessageLen is the longest user message we'll use as a fallback
// search query when no subjects are available.
const maxUserMessageLen = 100

// buildQuery constructs a search query from subjects and/or the user
// message. Content-shaped subject prefixes (entity:, zone:, area:,
// space:) are stripped to leave raw identifiers that match
// conversational references. Identity-shaped subjects (contact:) are
// dropped from the query because their values are stable handles
// (UUIDs, phone numbers, email addresses) that either match nothing
// or match every message from that handle — neither produces a
// useful FTS ranking.
//
// Returns the query string and a source label ("subjects" or
// "message_fallback") for logging.
func (p *ArchiveContextProvider) buildQuery(subjects []string, userMessage string) (string, string) {
	seen := make(map[string]bool)
	var terms []string

	for _, s := range subjects {
		if isIdentitySubject(s) {
			continue
		}
		term := stripSubjectPrefix(s)
		if term != "" && !seen[term] {
			seen[term] = true
			terms = append(terms, term)
		}
	}

	if len(terms) > 0 {
		return strings.Join(terms, " "), "subjects"
	}

	// Fall back to user message when no content-shaped subjects are
	// available. Only use short, single-line messages — long content
	// produces noisy queries. This branch handles two cases that look
	// the same from here: no subjects at all, and only identity-shaped
	// subjects (e.g. Signal contact handles) that we filtered out
	// above.
	if userMessage != "" {
		msg := strings.TrimSpace(userMessage)
		if len(msg) <= maxUserMessageLen && !strings.ContainsAny(msg, "\n\r") {
			return msg, "message_fallback"
		}
	}

	return "", ""
}

// identitySubjectPrefixes lists the subject kinds whose values are
// stable identity handles, not content terms. Stripping the prefix
// leaves a UUID, phone, or address that the FTS index treats as a
// generic token — typically matching every message from that
// participant and ranking the shortest of those highest. Useful for
// scoping (conversation_id filters, knowledge subject keys) but a
// pollutant in a full-text query.
var identitySubjectPrefixes = map[string]struct{}{
	"contact": {},
}

// isIdentitySubject reports whether a subject's prefix marks it as
// an identity handle rather than a content term.
func isIdentitySubject(s string) bool {
	idx := strings.IndexByte(s, ':')
	if idx < 0 {
		return false
	}
	_, ok := identitySubjectPrefixes[s[:idx]]
	return ok
}

// stripSubjectPrefix removes the type prefix from a subject string.
// "entity:light.office" → "light.office", "zone:kitchen" → "kitchen".
func stripSubjectPrefix(s string) string {
	if idx := strings.IndexByte(s, ':'); idx >= 0 {
		return s[idx+1:]
	}
	return s
}

// archiveSectionHeading is the markdown heading the prewarm block emits
// above its JSON payload. The heading is markdown framing (a section
// boundary); the payload below is typed JSON per
// docs/model-facing-context.md.
const archiveSectionHeading = "### Past Experience\n\n"

// Prewarm-specific projection bounds. These are tighter than
// FormatSearchResults' defaults because the prewarm block has a much
// smaller byte budget than archive_search/archive_range tool output
// (default 4 KB vs. the tool's full byte cap). Without trimming, a
// single normal hit can carry up to 5 context messages × ~2 KB each
// and overflow the prewarm budget on its own. With these caps, even
// the most context-rich hit comfortably fits one or two hits inside
// the default budget while preserving the JSON schema sibling
// providers emit.
const (
	prewarmContextPerSide    = 2
	prewarmMessageContentMax = 300
)

// formatResults renders archive search hits as a heading followed by a
// JSON projection produced by FormatSearchResults. Hits are first
// trimmed for the prewarm budget (smaller per-message content cap and
// fewer context messages per side than the archive tools use), then
// the hit list is shortened from the tail until the rendered output
// fits in p.maxBytes. The truncated flag in the JSON tells the model
// when hits were dropped. Returns empty string when there are no hits
// to render or when not even one trimmed hit fits in the byte budget.
func (p *ArchiveContextProvider) formatResults(results []SearchResult) string {
	if len(results) == 0 {
		return ""
	}
	now := time.Now()
	trimmed := trimResultsForPrewarm(results)

	for n := len(trimmed); n > 0; n-- {
		body := FormatSearchResults(trimmed[:n], now, n < len(trimmed))
		out := archiveSectionHeading + string(body)
		if len(out) <= p.maxBytes {
			if n < len(trimmed) {
				p.logger.Debug("archive pre-warm: trimmed to fit byte budget",
					"included", n,
					"total", len(trimmed),
					"budget", p.maxBytes,
					"size", len(out),
				)
			}
			return out
		}
	}

	p.logger.Debug("archive pre-warm: not even one hit fits byte budget",
		"total", len(trimmed),
		"budget", p.maxBytes,
	)
	return ""
}

// trimResultsForPrewarm returns a copy of results with each hit's
// context window narrowed to [prewarmContextPerSide] messages on each
// side and every message's content clipped to
// [prewarmMessageContentMax] bytes. This keeps a single hit small
// enough to fit comfortably under the typical prewarm byte budget;
// FormatSearchResults applies its own (larger) caps on top, so the
// tighter prewarm bounds win.
func trimResultsForPrewarm(results []SearchResult) []SearchResult {
	out := make([]SearchResult, len(results))
	for i, r := range results {
		before := r.ContextBefore
		if len(before) > prewarmContextPerSide {
			before = before[len(before)-prewarmContextPerSide:]
		}
		after := r.ContextAfter
		if len(after) > prewarmContextPerSide {
			after = after[:prewarmContextPerSide]
		}
		out[i] = SearchResult{
			SessionID:     r.SessionID,
			Match:         clipMessageContent(r.Match, prewarmMessageContentMax),
			ContextBefore: clipMessageSlice(before, prewarmMessageContentMax),
			ContextAfter:  clipMessageSlice(after, prewarmMessageContentMax),
			Highlight:     r.Highlight,
		}
	}
	return out
}

// clipMessageContent returns m with Content truncated to max bytes on
// a UTF-8 boundary. The original message is not mutated; the returned
// value is a shallow copy with the clipped Content.
func clipMessageContent(m Message, max int) Message {
	if len(m.Content) <= max {
		return m
	}
	m.Content = clipToRuneBoundary(m.Content, max)
	return m
}

// clipMessageSlice applies clipMessageContent to each entry, returning
// a new slice. Nil input returns nil.
func clipMessageSlice(msgs []Message, max int) []Message {
	if msgs == nil {
		return nil
	}
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		out[i] = clipMessageContent(m, max)
	}
	return out
}

// clipToRuneBoundary truncates s to at most max bytes, stepping back
// to the previous UTF-8 boundary if max falls inside a multi-byte
// rune so the returned string is always valid UTF-8.
func clipToRuneBoundary(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}

// previewContent shortens message content for debug logging. Output is
// not model-facing; the model sees the JSON projection from
// FormatSearchResults instead.
func previewContent(s string) string {
	const max = 80
	s = strings.TrimSpace(s)
	if s == "" {
		return "(empty)"
	}
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}
