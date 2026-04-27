package memory

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

	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// ArchiveReader is the subset of [ArchiveStore] needed by the episodic
// memory provider. Defined as an interface for testability.
type ArchiveReader interface {
	// ListSessions returns sessions ordered newest-first. Pass empty
	// conversationID to list sessions across all conversations.
	ListSessions(conversationID string, limit int) ([]*Session, error)
}

// EpisodicConfig holds configuration for the episodic memory provider.
type EpisodicConfig struct {
	// Timezone is the IANA timezone string (e.g. "America/Chicago").
	Timezone string

	// DailyDir is the directory containing YYYY-MM-DD.md daily memory
	// files. Supports ~ expansion. Empty disables daily file injection.
	DailyDir string

	// LookbackDays is how many days of daily memory files to include.
	LookbackDays int

	// HistoryTokens is the approximate token budget for recent
	// conversation history. Converted to a byte cap (×4) when fitting
	// the JSON catalog block.
	HistoryTokens int
}

// recentSessionsListLimit is the over-fetch from the archive before
// content / closed filtering. Twenty closed sessions with metadata is
// usually plenty even after non-content delegate sessions are dropped.
const recentSessionsListLimit = 20

// EpisodicProvider implements [agent.TagContextProvider] for
// episodic memory. It injects two unrelated context blocks into the
// system prompt:
//
//   - "Daily Notes" — markdown content from per-day notes files (a
//     human-authored journal) for the configured lookback window.
//
//   - "Recent Sessions" — a JSON catalog of the most recent closed
//     sessions across all conversations, keyed for archive_search and
//     archive_session_transcript follow-ups. Rendered via
//     [FormatSessionsList] for schema parity with the archive_*
//     tools and the message_channel context provider.
//
// Per-channel verbatim history (the model's "what did we just say?"
// view for message-channel conversations) lives in a separate provider
// gated on the message_channel capability tag — see
// [MessageChannelProvider]. EpisodicProvider intentionally stays
// channel-agnostic and emits the same JSON shape on every code path.
type EpisodicProvider struct {
	archive       ArchiveReader
	logger        *slog.Logger
	timezone      string
	dailyDir      string
	lookbackDays  int
	historyTokens int
	nowFunc       func() time.Time
}

// NewEpisodicProvider creates an episodic memory context provider.
func NewEpisodicProvider(archive ArchiveReader, logger *slog.Logger, cfg EpisodicConfig) *EpisodicProvider {
	return &EpisodicProvider{
		archive:       archive,
		logger:        logger,
		timezone:      cfg.Timezone,
		dailyDir:      cfg.DailyDir,
		lookbackDays:  cfg.LookbackDays,
		historyTokens: cfg.HistoryTokens,
		nowFunc:       time.Now,
	}
}

// TagContext returns episodic memory context for injection into the
// system prompt. It assembles daily memory notes and the recent-
// sessions JSON catalog from the archive. Implements
// [agent.TagContextProvider]; registered via
// RegisterAlwaysContextProvider.
func (p *EpisodicProvider) TagContext(_ context.Context, _ agentctx.ContextRequest) (string, error) {
	var sb strings.Builder

	daily := p.getDailyMemory()
	if daily != "" {
		sb.WriteString("### Daily Notes\n\n")
		sb.WriteString(daily)
	}

	recent := p.getRecentSessionsJSON()
	if len(recent) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("### Recent Sessions\n\n")
		sb.WriteString("Past conversation sessions, newest first. Use archive_session_transcript for full transcripts, or archive_search to search semantically.\n\n")
		sb.WriteString("```json\n")
		sb.Write(recent)
		sb.WriteString("\n```\n")
	}

	return sb.String(), nil
}

// getDailyMemory reads daily memory files for the configured lookback
// window and returns formatted content, or empty string if no files
// are found or dailyDir is not configured.
func (p *EpisodicProvider) getDailyMemory() string {
	if p.dailyDir == "" {
		return ""
	}

	dir := paths.ExpandHome(p.dailyDir)
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

// getRecentSessionsJSON returns the recent-sessions JSON catalog as
// raw bytes, or nil when there is nothing to emit. Closed sessions
// only; sessions without content (delegate runs with no metadata)
// are filtered out.
func (p *EpisodicProvider) getRecentSessionsJSON() []byte {
	if p.archive == nil {
		return nil
	}
	allSessions, err := p.archive.ListSessions("", recentSessionsListLimit)
	if err != nil {
		p.logger.Warn("episodic: failed to list sessions", "error", err)
		return nil
	}
	var sessions []*Session
	for _, s := range allSessions {
		if s.EndedAt == nil {
			continue
		}
		if !sessionHasContent(s) {
			continue
		}
		sessions = append(sessions, s)
	}
	if len(sessions) == 0 {
		return nil
	}

	// Convert the token budget to a byte cap (×4 ≈ 1 token / 4 bytes
	// rule of thumb). Drop oldest sessions first when the cap bites
	// — most recent are the most useful.
	byteCap := p.historyTokens * 4
	if byteCap <= 0 {
		byteCap = 16000
	}
	now := p.nowFunc()
	return FitPrefix(len(sessions), byteCap, func(k int) []byte {
		return FormatSessionsList(sessions[:k], now, k < len(sessions))
	})
}

// loadLocation returns the configured timezone or the system local
// timezone as a fallback.
func (p *EpisodicProvider) loadLocation() *time.Location {
	if p.timezone != "" {
		if loc, err := time.LoadLocation(p.timezone); err == nil {
			return loc
		}
	}
	return time.Now().Location()
}

// sessionHasContent reports whether a session has any meaningful
// content worth including in the recent-sessions catalog. Delegate
// sessions typically have no title, summary, or metadata and should
// be skipped. Sessions explicitly marked SessionType "empty" by the
// summarizer also drop out — they have metadata but no real transcript.
func sessionHasContent(sess *Session) bool {
	if sess.Metadata != nil && sess.Metadata.SessionType == "empty" {
		return false
	}
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
