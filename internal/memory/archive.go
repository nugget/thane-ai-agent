// Package memory provides conversation memory storage.
package memory

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ArchiveReason describes why messages were archived.
type ArchiveReason string

const (
	ArchiveReasonCompaction ArchiveReason = "compaction"
	ArchiveReasonReset      ArchiveReason = "reset"
	ArchiveReasonShutdown   ArchiveReason = "shutdown"
	ArchiveReasonManual     ArchiveReason = "manual"
)

// ArchiveStore handles immutable session transcript archiving.
type ArchiveStore struct {
	db *sql.DB

	// Whether FTS5 is available
	ftsEnabled bool

	// Context expansion defaults
	defaultSilenceThreshold time.Duration
	defaultMaxMessages      int
	defaultMaxDuration      time.Duration
}

// ArchiveConfig configures the archive store.
type ArchiveConfig struct {
	// SilenceThreshold is the gap duration that signals a conversation boundary.
	// Default: 10 minutes.
	SilenceThreshold time.Duration

	// MaxContextMessages is the hard cap on context messages per direction.
	// Default: 50.
	MaxContextMessages int

	// MaxContextDuration is the time-based hard cap on context expansion.
	// Default: 1 hour.
	MaxContextDuration time.Duration
}

// DefaultArchiveConfig returns sensible defaults.
func DefaultArchiveConfig() ArchiveConfig {
	return ArchiveConfig{
		SilenceThreshold:   10 * time.Minute,
		MaxContextMessages: 50,
		MaxContextDuration: 1 * time.Hour,
	}
}

// ArchivedMessage represents a message preserved in the archive.
type ArchivedMessage struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	SessionID      string    `json:"session_id"`
	Role           string    `json:"role"`
	Content        string    `json:"content"`
	Timestamp      time.Time `json:"timestamp"`
	TokenCount     int       `json:"token_count,omitempty"`
	ToolCalls      string    `json:"tool_calls,omitempty"`
	ToolCallID     string    `json:"tool_call_id,omitempty"`
	ArchivedAt     time.Time `json:"archived_at"`
	ArchiveReason  string    `json:"archive_reason"`
}

// Session represents a conversation session with boundaries.
type Session struct {
	ID             string     `json:"id"`
	ConversationID string     `json:"conversation_id"`
	StartedAt      time.Time  `json:"started_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	EndReason      string     `json:"end_reason,omitempty"`
	MessageCount   int        `json:"message_count"`
	Summary        string     `json:"summary,omitempty"`
}

// SearchResult represents a search hit with surrounding context.
type SearchResult struct {
	Match         ArchivedMessage   `json:"match"`
	SessionID     string            `json:"session_id"`
	ContextBefore []ArchivedMessage `json:"context_before"`
	ContextAfter  []ArchivedMessage `json:"context_after"`
	Highlight     string            `json:"highlight,omitempty"`
}

// SearchOptions configures a search query.
type SearchOptions struct {
	Query            string
	ConversationID   string        // optional filter
	SilenceThreshold time.Duration // gap that stops context expansion
	MaxMessages      int           // hard cap per direction
	MaxDuration      time.Duration // time-based cap per direction
	Limit            int           // max results
}

// NewArchiveStore creates a new archive store using the given database.
func NewArchiveStore(db *sql.DB, cfg ArchiveConfig) (*ArchiveStore, error) {
	if cfg.SilenceThreshold == 0 {
		cfg = DefaultArchiveConfig()
	}

	s := &ArchiveStore{
		db:                      db,
		defaultSilenceThreshold: cfg.SilenceThreshold,
		defaultMaxMessages:      cfg.MaxContextMessages,
		defaultMaxDuration:      cfg.MaxContextDuration,
	}

	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("archive migrate: %w", err)
	}

	// Try to enable FTS5 — gracefully degrade if not available
	s.ftsEnabled = s.tryEnableFTS()

	return s, nil
}

func (s *ArchiveStore) migrate() error {
	_, err := s.db.Exec(`
		-- Immutable archive of all messages
		CREATE TABLE IF NOT EXISTS archive_messages (
			id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			timestamp TIMESTAMP NOT NULL,
			token_count INTEGER DEFAULT 0,
			tool_calls TEXT,
			tool_call_id TEXT,
			archived_at TIMESTAMP NOT NULL,
			archive_reason TEXT NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_archive_conversation 
			ON archive_messages(conversation_id, timestamp);
		CREATE INDEX IF NOT EXISTS idx_archive_session 
			ON archive_messages(session_id, timestamp);
		CREATE INDEX IF NOT EXISTS idx_archive_timestamp 
			ON archive_messages(timestamp);
		CREATE INDEX IF NOT EXISTS idx_archive_reason 
			ON archive_messages(archive_reason);

		-- Session boundaries
		CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL,
			started_at TIMESTAMP NOT NULL,
			ended_at TIMESTAMP,
			end_reason TEXT,
			message_count INTEGER DEFAULT 0,
			summary TEXT
		);

		CREATE INDEX IF NOT EXISTS idx_sessions_conversation 
			ON sessions(conversation_id, started_at DESC);
		CREATE INDEX IF NOT EXISTS idx_sessions_started 
			ON sessions(started_at DESC);
	`)
	return err
}

// tryEnableFTS attempts to create the FTS5 virtual table.
// Returns true if FTS5 is available, false otherwise.
func (s *ArchiveStore) tryEnableFTS() bool {
	_, err := s.db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS archive_fts USING fts5(
			content,
			content=archive_messages,
			content_rowid=rowid
		);
	`)
	return err == nil
}

// ArchiveMessages copies messages to the immutable archive.
// This is the core "never throw data away" operation.
func (s *ArchiveStore) ArchiveMessages(messages []ArchivedMessage) error {
	if len(messages) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	insertMsg, err := tx.Prepare(`
		INSERT OR IGNORE INTO archive_messages 
			(id, conversation_id, session_id, role, content, timestamp, 
			 token_count, tool_calls, tool_call_id, archived_at, archive_reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer insertMsg.Close()

	// Prepare FTS sync statement (only if FTS5 is available)
	var insertFTS *sql.Stmt
	if s.ftsEnabled {
		insertFTS, err = tx.Prepare(`
			INSERT INTO archive_fts(rowid, content)
			SELECT rowid, content FROM archive_messages
			WHERE id = ?
		`)
		if err != nil {
			return fmt.Errorf("prepare fts: %w", err)
		}
		defer insertFTS.Close()
	}

	for _, m := range messages {
		if m.ID == "" {
			id, _ := uuid.NewV7()
			m.ID = id.String()
		}
		if m.ArchivedAt.IsZero() {
			m.ArchivedAt = time.Now().UTC()
		}

		result, err := insertMsg.Exec(
			m.ID, m.ConversationID, m.SessionID, m.Role, m.Content,
			m.Timestamp.Format(time.RFC3339Nano),
			m.TokenCount, nullString(m.ToolCalls), nullString(m.ToolCallID),
			m.ArchivedAt.Format(time.RFC3339Nano), m.ArchiveReason,
		)
		if err != nil {
			return fmt.Errorf("insert message %s: %w", m.ID, err)
		}

		// Only sync FTS if a row was actually inserted (not ignored as duplicate)
		affected, _ := result.RowsAffected()
		if affected > 0 && insertFTS != nil {
			if _, err := insertFTS.Exec(m.ID); err != nil {
				return fmt.Errorf("fts sync %s: %w", m.ID, err)
			}
		}
	}

	return tx.Commit()
}

// Search performs a full-text search with gap-aware context expansion.
func (s *ArchiveStore) Search(opts SearchOptions) ([]SearchResult, error) {
	if opts.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if opts.Limit <= 0 {
		opts.Limit = 10
	}
	if opts.SilenceThreshold == 0 {
		opts.SilenceThreshold = s.defaultSilenceThreshold
	}
	if opts.MaxMessages <= 0 {
		opts.MaxMessages = s.defaultMaxMessages
	}
	if opts.MaxDuration == 0 {
		opts.MaxDuration = s.defaultMaxDuration
	}

	// Build query — use FTS5 if available, fall back to LIKE
	var query string
	var args []any

	if s.ftsEnabled {
		query = `
			SELECT am.id, am.conversation_id, am.session_id, am.role, am.content,
			       am.timestamp, am.token_count, am.tool_calls, am.tool_call_id,
			       am.archived_at, am.archive_reason,
			       snippet(archive_fts, 0, '**', '**', '...', 64) as highlight
			FROM archive_fts
			JOIN archive_messages am ON archive_fts.rowid = am.rowid
		`
		args = []any{opts.Query}
		conditions := []string{"archive_fts MATCH ?"}

		if opts.ConversationID != "" {
			conditions = append(conditions, "am.conversation_id = ?")
			args = append(args, opts.ConversationID)
		}

		query += " WHERE " + strings.Join(conditions, " AND ")
		query += " ORDER BY rank LIMIT ?"
		args = append(args, opts.Limit)
	} else {
		// LIKE fallback — less precise but functional
		query = `
			SELECT id, conversation_id, session_id, role, content,
			       timestamp, token_count, tool_calls, tool_call_id,
			       archived_at, archive_reason,
			       '' as highlight
			FROM archive_messages
		`
		args = []any{"%" + opts.Query + "%"}
		conditions := []string{"content LIKE ?"}

		if opts.ConversationID != "" {
			conditions = append(conditions, "conversation_id = ?")
			args = append(args, opts.ConversationID)
		}

		query += " WHERE " + strings.Join(conditions, " AND ")
		query += " ORDER BY timestamp DESC LIMIT ?"
		args = append(args, opts.Limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	// Collect all matches first (must close rows before doing context expansion
	// queries, since SQLite may not support concurrent readers on same connection)
	type matchWithHighlight struct {
		msg       ArchivedMessage
		highlight string
	}
	var matches []matchWithHighlight

	for rows.Next() {
		var m ArchivedMessage
		var highlight string
		var tsStr, archivedStr string
		var toolCalls, toolCallID sql.NullString

		err := rows.Scan(
			&m.ID, &m.ConversationID, &m.SessionID, &m.Role, &m.Content,
			&tsStr, &m.TokenCount, &toolCalls, &toolCallID,
			&archivedStr, &m.ArchiveReason, &highlight,
		)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}

		m.Timestamp, _ = time.Parse(time.RFC3339Nano, tsStr)
		m.ArchivedAt, _ = time.Parse(time.RFC3339Nano, archivedStr)
		if toolCalls.Valid {
			m.ToolCalls = toolCalls.String
		}
		if toolCallID.Valid {
			m.ToolCallID = toolCallID.String
		}

		matches = append(matches, matchWithHighlight{msg: m, highlight: highlight})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate results: %w", err)
	}
	rows.Close()

	// Now expand context for each match (safe to query again)
	var results []SearchResult
	for _, mh := range matches {
		before := s.expandContext(mh.msg.ConversationID, mh.msg.Timestamp, true, opts)
		after := s.expandContext(mh.msg.ConversationID, mh.msg.Timestamp, false, opts)

		results = append(results, SearchResult{
			Match:         mh.msg,
			SessionID:     mh.msg.SessionID,
			ContextBefore: before,
			ContextAfter:  after,
			Highlight:     mh.highlight,
		})
	}

	return results, nil
}

// expandContext walks messages outward from a timestamp, stopping at silence gaps.
func (s *ArchiveStore) expandContext(
	conversationID string,
	from time.Time,
	backward bool,
	opts SearchOptions,
) []ArchivedMessage {
	var query string
	var boundary time.Time

	if backward {
		boundary = from.Add(-opts.MaxDuration)
		query = `
			SELECT id, conversation_id, session_id, role, content, timestamp,
			       token_count, tool_calls, tool_call_id, archived_at, archive_reason
			FROM archive_messages
			WHERE conversation_id = ? AND timestamp < ? AND timestamp > ?
			ORDER BY timestamp DESC
			LIMIT ?
		`
	} else {
		boundary = from.Add(opts.MaxDuration)
		query = `
			SELECT id, conversation_id, session_id, role, content, timestamp,
			       token_count, tool_calls, tool_call_id, archived_at, archive_reason
			FROM archive_messages
			WHERE conversation_id = ? AND timestamp > ? AND timestamp < ?
			ORDER BY timestamp ASC
			LIMIT ?
		`
	}

	fromStr := from.Format(time.RFC3339Nano)
	boundaryStr := boundary.Format(time.RFC3339Nano)

	rows, err := s.db.Query(query, conversationID, fromStr, boundaryStr, opts.MaxMessages)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var messages []ArchivedMessage
	prevTime := from

	for rows.Next() {
		var m ArchivedMessage
		var tsStr, archivedStr string
		var toolCalls, toolCallID sql.NullString

		err := rows.Scan(
			&m.ID, &m.ConversationID, &m.SessionID, &m.Role, &m.Content,
			&tsStr, &m.TokenCount, &toolCalls, &toolCallID,
			&archivedStr, &m.ArchiveReason,
		)
		if err != nil {
			continue
		}

		m.Timestamp, _ = time.Parse(time.RFC3339Nano, tsStr)
		m.ArchivedAt, _ = time.Parse(time.RFC3339Nano, archivedStr)
		if toolCalls.Valid {
			m.ToolCalls = toolCalls.String
		}
		if toolCallID.Valid {
			m.ToolCallID = toolCallID.String
		}

		// Check silence gap
		var gap time.Duration
		if backward {
			gap = prevTime.Sub(m.Timestamp)
		} else {
			gap = m.Timestamp.Sub(prevTime)
		}

		if gap > opts.SilenceThreshold {
			break // Hit a silence boundary
		}

		messages = append(messages, m)
		prevTime = m.Timestamp
	}

	// If we expanded backward, reverse so messages are chronological
	if backward {
		for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
			messages[i], messages[j] = messages[j], messages[i]
		}
	}

	return messages
}

// StartSession creates a new session record.
func (s *ArchiveStore) StartSession(conversationID string) (*Session, error) {
	id, _ := uuid.NewV7()
	now := time.Now().UTC()

	sess := &Session{
		ID:             id.String(),
		ConversationID: conversationID,
		StartedAt:      now,
	}

	_, err := s.db.Exec(`
		INSERT INTO sessions (id, conversation_id, started_at, message_count)
		VALUES (?, ?, ?, 0)
	`, sess.ID, conversationID, now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}

	return sess, nil
}

// EndSession marks a session as ended.
func (s *ArchiveStore) EndSession(sessionID string, reason string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE sessions SET ended_at = ?, end_reason = ? WHERE id = ?
	`, now.Format(time.RFC3339Nano), reason, sessionID)
	return err
}

// IncrementSessionCount bumps the message count for a session.
func (s *ArchiveStore) IncrementSessionCount(sessionID string) error {
	_, err := s.db.Exec(`
		UPDATE sessions SET message_count = message_count + 1 WHERE id = ?
	`, sessionID)
	return err
}

// ActiveSession returns the most recent unclosed session for a conversation, if any.
func (s *ArchiveStore) ActiveSession(conversationID string) (*Session, error) {
	row := s.db.QueryRow(`
		SELECT id, conversation_id, started_at, ended_at, end_reason, message_count, summary
		FROM sessions
		WHERE conversation_id = ? AND ended_at IS NULL
		ORDER BY started_at DESC
		LIMIT 1
	`, conversationID)

	return s.scanSession(row)
}

// GetSession retrieves a session by ID.
func (s *ArchiveStore) GetSession(sessionID string) (*Session, error) {
	row := s.db.QueryRow(`
		SELECT id, conversation_id, started_at, ended_at, end_reason, message_count, summary
		FROM sessions WHERE id = ?
	`, sessionID)

	return s.scanSession(row)
}

// ListSessions returns sessions, newest first.
func (s *ArchiveStore) ListSessions(conversationID string, limit int) ([]*Session, error) {
	if limit <= 0 {
		limit = 50
	}

	var query string
	var args []any

	if conversationID != "" {
		query = `
			SELECT id, conversation_id, started_at, ended_at, end_reason, message_count, summary
			FROM sessions WHERE conversation_id = ?
			ORDER BY started_at DESC LIMIT ?
		`
		args = []any{conversationID, limit}
	} else {
		query = `
			SELECT id, conversation_id, started_at, ended_at, end_reason, message_count, summary
			FROM sessions
			ORDER BY started_at DESC LIMIT ?
		`
		args = []any{limit}
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		sess, err := s.scanSessionRow(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}

	return sessions, rows.Err()
}

// GetSessionTranscript returns all archived messages for a session in chronological order.
func (s *ArchiveStore) GetSessionTranscript(sessionID string) ([]ArchivedMessage, error) {
	rows, err := s.db.Query(`
		SELECT id, conversation_id, session_id, role, content, timestamp,
		       token_count, tool_calls, tool_call_id, archived_at, archive_reason
		FROM archive_messages
		WHERE session_id = ?
		ORDER BY timestamp ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get transcript: %w", err)
	}
	defer rows.Close()

	return s.scanMessages(rows)
}

// GetMessagesByTimeRange returns archived messages within a time range.
func (s *ArchiveStore) GetMessagesByTimeRange(from, to time.Time, conversationID string, limit int) ([]ArchivedMessage, error) {
	if limit <= 0 {
		limit = 500
	}

	var query string
	var args []any

	if conversationID != "" {
		query = `
			SELECT id, conversation_id, session_id, role, content, timestamp,
			       token_count, tool_calls, tool_call_id, archived_at, archive_reason
			FROM archive_messages
			WHERE conversation_id = ? AND timestamp >= ? AND timestamp <= ?
			ORDER BY timestamp ASC
			LIMIT ?
		`
		args = []any{conversationID, from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano), limit}
	} else {
		query = `
			SELECT id, conversation_id, session_id, role, content, timestamp,
			       token_count, tool_calls, tool_call_id, archived_at, archive_reason
			FROM archive_messages
			WHERE timestamp >= ? AND timestamp <= ?
			ORDER BY timestamp ASC
			LIMIT ?
		`
		args = []any{from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano), limit}
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query by time range: %w", err)
	}
	defer rows.Close()

	return s.scanMessages(rows)
}

// ExportSessionMarkdown exports a session transcript as human-readable markdown.
func (s *ArchiveStore) ExportSessionMarkdown(sessionID string) (string, error) {
	sess, err := s.GetSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("get session: %w", err)
	}

	messages, err := s.GetSessionTranscript(sessionID)
	if err != nil {
		return "", fmt.Errorf("get transcript: %w", err)
	}

	var sb strings.Builder

	// Header
	sb.WriteString(fmt.Sprintf("# Session %s\n\n", sessionID[:8]))
	sb.WriteString(fmt.Sprintf("**Conversation:** %s\n", sess.ConversationID))
	sb.WriteString(fmt.Sprintf("**Started:** %s\n", sess.StartedAt.Format("2006-01-02 15:04:05 MST")))
	if sess.EndedAt != nil {
		sb.WriteString(fmt.Sprintf("**Ended:** %s (%s)\n", sess.EndedAt.Format("2006-01-02 15:04:05 MST"), sess.EndReason))
	}
	sb.WriteString(fmt.Sprintf("**Messages:** %d\n\n", len(messages)))
	sb.WriteString("---\n\n")

	// Messages
	for _, m := range messages {
		ts := m.Timestamp.Format("15:04:05")
		role := strings.ToUpper(m.Role[:1]) + m.Role[1:]

		switch m.Role {
		case "user":
			sb.WriteString(fmt.Sprintf("### 🧑 %s [%s]\n\n%s\n\n", role, ts, m.Content))
		case "assistant":
			sb.WriteString(fmt.Sprintf("### 🤖 %s [%s]\n\n%s\n\n", role, ts, m.Content))
		case "system":
			sb.WriteString(fmt.Sprintf("### ⚙️ %s [%s]\n\n%s\n\n", role, ts, m.Content))
		case "tool":
			name := m.ToolCallID
			if name == "" {
				name = "tool"
			}
			sb.WriteString(fmt.Sprintf("### 🔧 %s [%s]\n\n```\n%s\n```\n\n", name, ts, m.Content))
		default:
			sb.WriteString(fmt.Sprintf("### %s [%s]\n\n%s\n\n", role, ts, m.Content))
		}
	}

	return sb.String(), nil
}

// Stats returns archive statistics.
func (s *ArchiveStore) Stats() (map[string]any, error) {
	stats := make(map[string]any)

	var msgCount, sessionCount int
	var oldestStr, newestStr sql.NullString

	_ = s.db.QueryRow(`SELECT COUNT(*) FROM archive_messages`).Scan(&msgCount)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessionCount)
	_ = s.db.QueryRow(`SELECT MIN(timestamp) FROM archive_messages`).Scan(&oldestStr)
	_ = s.db.QueryRow(`SELECT MAX(timestamp) FROM archive_messages`).Scan(&newestStr)

	stats["total_messages"] = msgCount
	stats["total_sessions"] = sessionCount

	if oldestStr.Valid {
		stats["oldest_message"] = oldestStr.String
	}
	if newestStr.Valid {
		stats["newest_message"] = newestStr.String
	}

	// Messages by role
	byRole := make(map[string]int)
	rows, err := s.db.Query(`SELECT role, COUNT(*) FROM archive_messages GROUP BY role`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var role string
			var count int
			if err := rows.Scan(&role, &count); err == nil {
				byRole[role] = count
			}
		}
	}
	stats["by_role"] = byRole

	// Messages by reason
	byReason := make(map[string]int)
	rows2, err := s.db.Query(`SELECT archive_reason, COUNT(*) FROM archive_messages GROUP BY archive_reason`)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var reason string
			var count int
			if err := rows2.Scan(&reason, &count); err == nil {
				byReason[reason] = count
			}
		}
	}
	stats["by_reason"] = byReason

	return stats, nil
}

// --- helpers ---

func (s *ArchiveStore) scanSession(row *sql.Row) (*Session, error) {
	var sess Session
	var startStr string
	var endStr, endReason, summary sql.NullString

	err := row.Scan(&sess.ID, &sess.ConversationID, &startStr,
		&endStr, &endReason, &sess.MessageCount, &summary)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	sess.StartedAt, _ = time.Parse(time.RFC3339Nano, startStr)
	if endStr.Valid {
		t, _ := time.Parse(time.RFC3339Nano, endStr.String)
		sess.EndedAt = &t
	}
	if endReason.Valid {
		sess.EndReason = endReason.String
	}
	if summary.Valid {
		sess.Summary = summary.String
	}

	return &sess, nil
}

func (s *ArchiveStore) scanSessionRow(rows *sql.Rows) (*Session, error) {
	var sess Session
	var startStr string
	var endStr, endReason, summary sql.NullString

	err := rows.Scan(&sess.ID, &sess.ConversationID, &startStr,
		&endStr, &endReason, &sess.MessageCount, &summary)
	if err != nil {
		return nil, err
	}

	sess.StartedAt, _ = time.Parse(time.RFC3339Nano, startStr)
	if endStr.Valid {
		t, _ := time.Parse(time.RFC3339Nano, endStr.String)
		sess.EndedAt = &t
	}
	if endReason.Valid {
		sess.EndReason = endReason.String
	}
	if summary.Valid {
		sess.Summary = summary.String
	}

	return &sess, nil
}

func (s *ArchiveStore) scanMessages(rows *sql.Rows) ([]ArchivedMessage, error) {
	var messages []ArchivedMessage
	for rows.Next() {
		var m ArchivedMessage
		var tsStr, archivedStr string
		var toolCalls, toolCallID sql.NullString

		err := rows.Scan(
			&m.ID, &m.ConversationID, &m.SessionID, &m.Role, &m.Content,
			&tsStr, &m.TokenCount, &toolCalls, &toolCallID,
			&archivedStr, &m.ArchiveReason,
		)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}

		m.Timestamp, _ = time.Parse(time.RFC3339Nano, tsStr)
		m.ArchivedAt, _ = time.Parse(time.RFC3339Nano, archivedStr)
		if toolCalls.Valid {
			m.ToolCalls = toolCalls.String
		}
		if toolCallID.Valid {
			m.ToolCallID = toolCallID.String
		}

		messages = append(messages, m)
	}
	return messages, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
