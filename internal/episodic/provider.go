// Package episodic provides context injection of episodic memory into
// the agent's system prompt. It combines curated daily memory files
// with recency-graded conversation history from the session archive,
// giving the agent continuity across sessions.
package episodic

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/prompts"
)

// ArchiveReader is the subset of [memory.ArchiveStore] needed by the
// episodic memory provider. Defined as an interface for testability.
type ArchiveReader interface {
	// ListSessions returns sessions ordered newest-first. Pass empty
	// conversationID to list sessions across all conversations.
	ListSessions(conversationID string, limit int) ([]*memory.Session, error)

	// GetSessionTranscript returns all archived messages for a session
	// in chronological order.
	GetSessionTranscript(sessionID string) ([]memory.ArchivedMessage, error)
}

// Config holds configuration for the episodic memory provider.
type Config struct {
	// Timezone is the IANA timezone string (e.g. "America/Chicago").
	Timezone string

	// DailyDir is the directory containing YYYY-MM-DD.md daily memory
	// files. Supports ~ expansion. Empty disables daily file injection.
	DailyDir string

	// LookbackDays is how many days of daily memory files to include.
	LookbackDays int

	// HistoryTokens is the approximate token budget for recent
	// conversation history.
	HistoryTokens int

	// SessionGapMinutes is the silence duration between sessions that
	// triggers a gap annotation in the output.
	SessionGapMinutes int
}

// Provider implements the agent.ContextProvider interface for episodic
// memory. It injects daily memory notes and recent conversation history
// into the system prompt.
type Provider struct {
	archive       ArchiveReader
	logger        *slog.Logger
	timezone      string
	dailyDir      string
	lookbackDays  int
	historyTokens int
	sessionGap    time.Duration
	nowFunc       func() time.Time // injectable for testing; defaults to time.Now
}

// NewProvider creates an episodic memory context provider.
func NewProvider(archive ArchiveReader, logger *slog.Logger, cfg Config) *Provider {
	return &Provider{
		archive:       archive,
		logger:        logger,
		timezone:      cfg.Timezone,
		dailyDir:      cfg.DailyDir,
		lookbackDays:  cfg.LookbackDays,
		historyTokens: cfg.HistoryTokens,
		sessionGap:    time.Duration(cfg.SessionGapMinutes) * time.Minute,
		nowFunc:       time.Now,
	}
}

// GetContext returns episodic memory context for injection into the
// system prompt. It assembles daily memory notes and recent
// conversation history from the session archive.
func (p *Provider) GetContext(_ context.Context, _ string) (string, error) {
	var sb strings.Builder

	daily := p.getDailyMemory()
	if daily != "" {
		sb.WriteString("### Daily Notes\n\n")
		sb.WriteString(daily)
	}

	history := p.getRecentHistory()
	if history != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("### Recent Conversations\n\n")
		sb.WriteString(history)
	}

	return sb.String(), nil
}

// getDailyMemory reads daily memory files for the configured lookback
// window and returns formatted content, or empty string if no files
// are found or dailyDir is not configured.
func (p *Provider) getDailyMemory() string {
	if p.dailyDir == "" {
		return ""
	}

	dir := expandTilde(p.dailyDir)
	loc := p.loadLocation()
	now := p.nowFunc().In(loc)

	var sb strings.Builder
	for i := range p.lookbackDays {
		day := now.AddDate(0, 0, -i)
		filename := day.Format("2006-01-02") + ".md"
		path := filepath.Join(dir, filename)

		data, err := os.ReadFile(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				p.logger.Warn("daily memory file unreadable",
					"path", path, "error", err)
			}
			continue
		}

		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}

		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}

		label := dayLabel(i, day)
		sb.WriteString(fmt.Sprintf("**%s (%s):**\n", label, day.Format("2006-01-02")))
		sb.WriteString(content)
	}

	return sb.String()
}

// getRecentHistory assembles recent conversation history from the
// session archive using a recency gradient: most recent session gets
// transcript excerpts, recent sessions get paragraph summaries, and
// older sessions get one-liners. Fills backward until the token budget
// is exhausted.
func (p *Provider) getRecentHistory() string {
	if p.archive == nil {
		return ""
	}

	allSessions, err := p.archive.ListSessions("", 20)
	if err != nil {
		p.logger.Warn("episodic: failed to list sessions", "error", err)
		return ""
	}

	// Filter to closed sessions only. Active (unclosed) sessions have
	// EndedAt == nil — they represent the current conversation or
	// other in-progress sessions whose transcripts are incomplete.
	var sessions []*memory.Session
	for _, s := range allSessions {
		if s.EndedAt != nil {
			sessions = append(sessions, s)
		}
	}
	if len(sessions) == 0 {
		return ""
	}

	framing := prompts.EpisodicHistoryFraming() + "\n\n"
	budget := p.historyTokens - estimateTokens(framing)
	var entries []string

	formatted := 0          // Count of sessions actually emitted.
	var prevStart time.Time // StartedAt of the previously emitted session.
	for _, sess := range sessions {
		if budget <= 0 {
			break
		}

		// Skip sessions with no useful content (e.g. delegate sessions
		// with no title, summary, or metadata).
		if !sessionHasContent(sess) {
			continue
		}

		// Gap detection between consecutively emitted sessions.
		var gapNote string
		if formatted > 0 && sess.EndedAt != nil {
			gap := prevStart.Sub(*sess.EndedAt)
			if gap > p.sessionGap {
				gapNote = fmt.Sprintf("*(%s gap)*\n", formatGap(gap))
			}
		}

		var entry string
		var cost int

		switch {
		case formatted == 0:
			// Most recent archived session: transcript excerpt.
			entry, cost = p.formatTranscriptExcerpt(sess, budget)
		case formatted <= 3:
			// Recent sessions: paragraph summary.
			entry, cost = p.formatParagraph(sess)
		default:
			// Older sessions: one-liner.
			entry, cost = p.formatOneLiner(sess)
		}

		if cost > budget && formatted > 0 {
			break
		}

		if gapNote != "" {
			entries = append(entries, gapNote)
		}
		entries = append(entries, entry)
		budget -= cost
		prevStart = sess.StartedAt
		formatted++
	}

	if len(entries) == 0 {
		return ""
	}

	// Reverse to chronological order (entries are newest-first).
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	var sb strings.Builder
	sb.WriteString(framing)
	for _, e := range entries {
		sb.WriteString(e)
		if !strings.HasSuffix(e, "\n") {
			sb.WriteString("\n")
		}
	}

	return strings.TrimSpace(sb.String())
}

// maxExcerptMessages caps how many messages we scan from the tail of a
// session transcript. This avoids loading excessively large transcripts
// when the budget only needs a handful of messages. A future
// optimisation could add a GetRecentMessages(sessionID, limit) method
// to ArchiveReader to push this limit into the SQL query.
const maxExcerptMessages = 50

// formatTranscriptExcerpt formats the most recent session with actual
// message excerpts from the transcript, walking backward from the end
// until the budget is consumed.
func (p *Provider) formatTranscriptExcerpt(sess *memory.Session, budget int) (string, int) {
	header := p.sessionHeader(sess)

	messages, err := p.archive.GetSessionTranscript(sess.ID)
	if err != nil || len(messages) == 0 {
		return p.formatParagraph(sess)
	}

	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString("\n")

	remaining := budget - estimateTokens(header)

	// Collect user/assistant messages from the end, capped to avoid
	// scanning excessively long transcripts.
	loc := p.loadLocation()
	var excerpts []string
	scanned := 0
	for i := len(messages) - 1; i >= 0 && remaining > 0 && scanned < maxExcerptMessages; i-- {
		scanned++
		m := messages[i]
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		ts := m.Timestamp.In(loc).Format(time.RFC3339)
		line := fmt.Sprintf("[%s] **%s:** %s", ts, m.Role, truncateContent(m.Content, 200))
		cost := estimateTokens(line)
		if cost > remaining {
			break
		}
		excerpts = append(excerpts, line)
		remaining -= cost
	}

	// Reverse to chronological order.
	for i, j := 0, len(excerpts)-1; i < j; i, j = i+1, j-1 {
		excerpts[i], excerpts[j] = excerpts[j], excerpts[i]
	}

	for _, e := range excerpts {
		sb.WriteString(e)
		sb.WriteString("\n")
	}

	result := sb.String()
	return result, estimateTokens(result)
}

// formatParagraph formats a session with its paragraph-level summary.
func (p *Provider) formatParagraph(sess *memory.Session) (string, int) {
	header := p.sessionHeader(sess)
	summary := sessionParagraph(sess)
	entry := header + summary + "\n"
	return entry, estimateTokens(entry)
}

// formatOneLiner formats a session with a single-line summary.
func (p *Provider) formatOneLiner(sess *memory.Session) (string, int) {
	header := p.sessionHeader(sess)
	oneliner := sessionOneLiner(sess)
	entry := header + oneliner + "\n"
	return entry, estimateTokens(entry)
}

// sessionHeader returns a formatted header for a session entry.
func (p *Provider) sessionHeader(sess *memory.Session) string {
	loc := p.loadLocation()
	ts := sess.StartedAt.In(loc).Format(time.RFC3339)
	title := ""
	if sess.Title != "" {
		title = " — " + sess.Title
	}
	return fmt.Sprintf("**[%s%s]** ", ts, title)
}

// loadLocation returns the configured timezone or the system local
// timezone as a fallback.
func (p *Provider) loadLocation() *time.Location {
	if p.timezone != "" {
		if loc, err := time.LoadLocation(p.timezone); err == nil {
			return loc
		}
	}
	return time.Now().Location()
}

// sessionParagraph returns the best available paragraph-level summary
// for a session, falling through a chain of alternatives.
func sessionParagraph(sess *memory.Session) string {
	if sess.Metadata != nil && sess.Metadata.Paragraph != "" {
		return sess.Metadata.Paragraph
	}
	if sess.Summary != "" {
		return sess.Summary
	}
	if sess.Metadata != nil && sess.Metadata.OneLiner != "" {
		return sess.Metadata.OneLiner
	}
	if sess.Title != "" {
		return sess.Title
	}
	return "(no summary available)"
}

// sessionHasContent reports whether a session has any meaningful content
// worth including in the episodic history. Delegate sessions typically
// have no title, summary, or metadata and should be skipped.
func sessionHasContent(sess *memory.Session) bool {
	if sess.Title != "" {
		return true
	}
	if sess.Summary != "" {
		return true
	}
	if sess.Metadata != nil {
		if sess.Metadata.OneLiner != "" || sess.Metadata.Paragraph != "" || sess.Metadata.Detailed != "" {
			return true
		}
	}
	return false
}

// sessionOneLiner returns the best available one-line summary for a
// session.
func sessionOneLiner(sess *memory.Session) string {
	if sess.Metadata != nil && sess.Metadata.OneLiner != "" {
		return sess.Metadata.OneLiner
	}
	if sess.Title != "" {
		return sess.Title
	}
	if sess.Summary != "" {
		return firstSentence(sess.Summary)
	}
	return "(no summary)"
}

// dayLabel returns a human-readable label for a day offset (0 = today,
// 1 = yesterday, etc.).
func dayLabel(offset int, day time.Time) string {
	switch offset {
	case 0:
		return "Today"
	case 1:
		return "Yesterday"
	default:
		return day.Format("Monday, January 2")
	}
}

// estimateTokens returns a rough token estimate for a string. Uses the
// len/4 heuristic matching the convention in the memory package.
func estimateTokens(s string) int {
	return len(s) / 4
}

// expandTilde replaces a leading ~ with the user's home directory.
func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		return filepath.Join(home, path[2:])
	}
	return path
}

// truncateContent shortens a string to maxLen runes, appending "..."
// if truncated. Newlines are replaced with spaces for inline display.
func truncateContent(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLen]) + "..."
}

// firstSentence returns the first sentence of a string, or a truncated
// version if no sentence boundary is found. Truncation is rune-safe.
func firstSentence(s string) string {
	if idx := strings.Index(s, ". "); idx > 0 && idx < 100 {
		return s[:idx+1]
	}
	if utf8.RuneCountInString(s) > 80 {
		runes := []rune(s)
		return string(runes[:80]) + "..."
	}
	return s
}

// formatGap returns a human-readable representation of a time gap
// between sessions.
func formatGap(d time.Duration) string {
	hours := int(d.Hours())
	if hours >= 24 {
		days := hours / 24
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}
