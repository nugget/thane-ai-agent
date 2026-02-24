package memory

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/facts"
)

// ArchiveSearcher abstracts archive search for testing. ArchiveStore
// satisfies this interface — no adapter needed.
type ArchiveSearcher interface {
	Search(opts SearchOptions) ([]SearchResult, error)
}

// ArchiveContextProvider implements the agent.ContextProvider interface
// for injecting relevant past conversation excerpts into the system
// prompt. This is Layer 2 of the pre-warming system: Layer 1 provides
// knowledge (facts + KB docs), Layer 2 provides experiential judgment
// (prior reasoning about similar situations).
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

// GetContext searches the conversation archive for excerpts relevant to
// the current wake context. Subjects are extracted from ctx (set by
// the wake bridge); if no subjects are available but the user message
// is short, it falls back to searching by message content.
//
// Returns empty string when there is nothing to search for or no results
// are found. Errors from the archive store are logged and swallowed —
// archive injection should never block a wake.
func (p *ArchiveContextProvider) GetContext(ctx context.Context, userMessage string) (string, error) {
	subjects := facts.SubjectsFromContext(ctx)

	query := p.buildQuery(subjects, userMessage)
	if query == "" {
		return "", nil
	}

	results, err := p.store.Search(SearchOptions{
		Query: query,
		Limit: p.maxResults,
	})
	if err != nil {
		p.logger.Warn("archive pre-warm search failed",
			"query", query,
			"error", err,
		)
		return "", nil
	}
	if len(results) == 0 {
		p.logger.Debug("archive pre-warm: no results",
			"query", query,
		)
		return "", nil
	}

	p.logger.Debug("archive pre-warm: injecting results",
		"query", query,
		"results", len(results),
	)
	return p.formatResults(results), nil
}

// maxUserMessageLen is the longest user message we'll use as a fallback
// search query when no subjects are available.
const maxUserMessageLen = 100

// buildQuery constructs a search query from subjects and/or the user
// message. Subject prefixes (entity:, zone:, etc.) are stripped to
// leave raw identifiers that match conversational references.
func (p *ArchiveContextProvider) buildQuery(subjects []string, userMessage string) string {
	seen := make(map[string]bool)
	var terms []string

	for _, s := range subjects {
		term := stripSubjectPrefix(s)
		if term != "" && !seen[term] {
			seen[term] = true
			terms = append(terms, term)
		}
	}

	// Fall back to user message when no subjects are available.
	// Only use short, single-line messages — long content produces
	// noisy queries.
	if len(terms) == 0 && userMessage != "" {
		msg := strings.TrimSpace(userMessage)
		if len(msg) <= maxUserMessageLen && !strings.ContainsAny(msg, "\n\r") {
			return msg
		}
	}

	return strings.Join(terms, " ")
}

// stripSubjectPrefix removes the type prefix from a subject string.
// "entity:light.office" → "light.office", "zone:kitchen" → "kitchen".
func stripSubjectPrefix(s string) string {
	if idx := strings.IndexByte(s, ':'); idx >= 0 {
		return s[idx+1:]
	}
	return s
}

// maxMessageChars is the per-message content truncation limit for
// formatted archive results.
const maxMessageChars = 200

// formatResults renders archive search results as markdown suitable for
// system prompt injection. Output is capped at p.maxBytes; if results
// are truncated, a note indicates how many were omitted.
func (p *ArchiveContextProvider) formatResults(results []SearchResult) string {
	var sb strings.Builder
	sb.WriteString("### Past Experience\n\n")

	included := 0
	for i, r := range results {
		var block strings.Builder
		block.WriteString(formatResultBlock(r))
		if i < len(results)-1 {
			block.WriteByte('\n')
		}

		// Check byte budget before adding this block. If it won't fit,
		// append a truncation notice only if that itself fits.
		if sb.Len()+block.Len() > p.maxBytes {
			remaining := len(results) - included
			truncationMsg := fmt.Sprintf(
				"*(%d additional result(s) omitted — byte budget reached)*\n",
				remaining,
			)
			if sb.Len()+len(truncationMsg) <= p.maxBytes {
				sb.WriteString(truncationMsg)
			}
			break
		}

		sb.WriteString(block.String())
		included++
	}

	if included == 0 {
		return ""
	}

	return sb.String()
}

// formatResultBlock renders a single search result as a markdown block
// with session date, matched message (bolded), and surrounding context.
func formatResultBlock(r SearchResult) string {
	var sb strings.Builder

	// Header: date and session ID prefix.
	sessionShort := r.SessionID
	if len(sessionShort) > 8 {
		sessionShort = sessionShort[:8]
	}
	date := r.Match.Timestamp.Format(time.DateOnly)
	sb.WriteString(fmt.Sprintf("**%s — Session %s:**\n", date, sessionShort))

	// Context before (up to 2 messages for brevity).
	before := r.ContextBefore
	if len(before) > 2 {
		before = before[len(before)-2:]
	}
	for _, m := range before {
		sb.WriteString(fmt.Sprintf("> [%s] %s\n", m.Role, truncateContent(m.Content)))
	}

	// Matched message — bolded.
	sb.WriteString(fmt.Sprintf("> **[%s] %s**\n", r.Match.Role, truncateContent(r.Match.Content)))

	// Context after (up to 2 messages for brevity).
	after := r.ContextAfter
	if len(after) > 2 {
		after = after[:2]
	}
	for _, m := range after {
		sb.WriteString(fmt.Sprintf("> [%s] %s\n", m.Role, truncateContent(m.Content)))
	}

	return sb.String()
}

// truncateContent shortens message content for display, preserving the
// first line and trimming at maxMessageChars.
func truncateContent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(empty)"
	}

	// Collapse to first line if multi-line.
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx] + "..."
	}

	if len(s) > maxMessageChars {
		// Respect UTF-8 boundary by truncating at last space before limit.
		if idx := strings.LastIndexByte(s[:maxMessageChars], ' '); idx > maxMessageChars/2 {
			s = s[:idx] + "..."
		} else {
			s = s[:maxMessageChars] + "..."
		}
	}

	return s
}
