package web

import (
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/nugget/thane-ai-agent/internal/logging"
)

// SessionLogsData is the template data for the session_logs.html partial.
type SessionLogsData struct {
	PageData
	SessionID string
	Entries   []*logEntryRow
	Level     string // current filter value
	Subsystem string // current filter value
}

// logEntryRow holds a single log entry formatted for display.
type logEntryRow struct {
	Timestamp  string
	Level      string
	Msg        string
	Subsystem  string
	Tool       string
	Source     string // "file:line"
	Attrs      string // formatted JSON
	LevelClass string // CSS badge class
}

// handleSessionLogs returns an HTML partial with log entries for a session.
// It is loaded via HTMX into the session detail page.
func (s *WebServer) handleSessionLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.logStore == nil {
		http.Error(w, "log index not available", http.StatusServiceUnavailable)
		return
	}

	level := r.URL.Query().Get("level")
	subsystem := r.URL.Query().Get("subsystem")

	entries, err := s.logStore.QueryBySession(id, level, subsystem, 500)
	if err != nil {
		s.logger.Error("failed to query session logs", "session_id", id, "error", err)
		http.Error(w, "failed to query logs", http.StatusInternalServerError)
		return
	}

	data := SessionLogsData{
		PageData: PageData{
			BrandName: s.brandName,
			ActiveNav: "sessions",
		},
		SessionID: id,
		Entries:   logEntriesToRows(entries),
		Level:     level,
		Subsystem: subsystem,
	}

	s.renderBlock(w, "session_logs.html", "content", data)
}

// logEntriesToRows converts logging.LogEntry values to template-ready rows.
func logEntriesToRows(entries []logging.LogEntry) []*logEntryRow {
	rows := make([]*logEntryRow, 0, len(entries))
	for _, e := range entries {
		row := &logEntryRow{
			Timestamp:  e.Timestamp.Format(time.DateTime),
			Level:      e.Level,
			Msg:        e.Msg,
			Subsystem:  e.Subsystem,
			Tool:       e.Tool,
			Attrs:      e.Attrs,
			LevelClass: levelBadgeClass(e.Level),
		}
		if e.SourceFile != "" {
			row.Source = fmt.Sprintf("%s:%d", filepath.Base(e.SourceFile), e.SourceLine)
		}
		rows = append(rows, row)
	}
	return rows
}

// levelBadgeClass returns the CSS badge class for the given log level.
func levelBadgeClass(level string) string {
	switch level {
	case "ERROR":
		return "badge-err"
	case "WARN":
		return "badge-warn"
	case "INFO":
		return "badge-ok"
	default:
		return "badge-muted"
	}
}
