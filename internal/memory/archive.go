package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
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
	db     *sql.DB
	logger *slog.Logger

	// Message storage routing. In unified mode (messagesDB != nil), message
	// queries go to the working DB against the "messages" table. In legacy
	// mode (messagesDB == nil), they go to archive.db's "archive_messages".
	// Set once at construction time — never changes.
	messagesDB   *sql.DB
	msgTableName string // "messages" or "archive_messages"
	msgFTSName   string // "messages_fts" or "archive_fts"

	// Tool call storage routing. Follows the same pattern as messages.
	// In unified mode, tool call queries use the working DB's "tool_calls"
	// table. In legacy mode, they use archive.db's "archive_tool_calls".
	tcTableName string // "tool_calls" or "archive_tool_calls"

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
	ID               string           `json:"id"`
	ConversationID   string           `json:"conversation_id"`
	StartedAt        time.Time        `json:"started_at"`
	EndedAt          *time.Time       `json:"ended_at,omitempty"`
	EndReason        string           `json:"end_reason,omitempty"`
	MessageCount     int              `json:"message_count"`
	Summary          string           `json:"summary,omitempty"`
	Title            string           `json:"title,omitempty"`
	Tags             []string         `json:"tags,omitempty"`
	Metadata         *SessionMetadata `json:"metadata,omitempty"`
	ParentSessionID  string           `json:"parent_session_id,omitempty"`
	ParentToolCallID string           `json:"parent_tool_call_id,omitempty"`
}

// SessionOption configures optional fields when starting a session.
type SessionOption func(*Session)

// WithParentSession sets the parent session ID for a child session
// (e.g. a delegate spawned from a parent session).
func WithParentSession(id string) SessionOption {
	return func(s *Session) { s.ParentSessionID = id }
}

// WithParentToolCall sets the tool call ID that triggered this child
// session (e.g. the thane_delegate tool call in the parent).
func WithParentToolCall(id string) SessionOption {
	return func(s *Session) { s.ParentToolCallID = id }
}

// SessionMetadata holds rich, LLM-generated metadata for human-oriented
// search and browsing. Stored as JSON in the database for flexibility —
// new fields can be added without schema migrations.
type SessionMetadata struct {
	// Summaries at different lengths for different display contexts.
	OneLiner  string `json:"one_liner,omitempty"` // ~10 words
	Paragraph string `json:"paragraph,omitempty"` // 2-4 sentences
	Detailed  string `json:"detailed,omitempty"`  // full summary

	// Key decisions or outcomes from the session.
	KeyDecisions []string `json:"key_decisions,omitempty"`

	// People involved or mentioned.
	Participants []string `json:"participants,omitempty"`

	// Characterization of the session's nature.
	SessionType string `json:"session_type,omitempty"` // e.g. "debugging", "architecture", "philosophy", "casual"

	// Tools used during the session (tool name → call count).
	ToolsUsed map[string]int `json:"tools_used,omitempty"`

	// Files touched or discussed during the session.
	FilesTouched []string `json:"files_touched,omitempty"`

	// Model(s) used, if known.
	Models []string `json:"models,omitempty"`
}

// ArchivedToolCall represents a tool call preserved in the archive.
type ArchivedToolCall struct {
	ID             string     `json:"id"`
	ConversationID string     `json:"conversation_id"`
	SessionID      string     `json:"session_id"`
	ToolName       string     `json:"tool_name"`
	Arguments      string     `json:"arguments"`
	Result         string     `json:"result,omitempty"`
	Error          string     `json:"error,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	DurationMs     int64      `json:"duration_ms,omitempty"`
	ArchivedAt     time.Time  `json:"archived_at"`
	IterationIndex *int       `json:"iteration_index,omitempty"`
}

// ArchivedIteration represents one pass through an agent or delegate loop
// preserved in the archive. Each iteration corresponds to one LLM call
// plus any tool calls that follow.
type ArchivedIteration struct {
	SessionID      string    `json:"session_id"`
	IterationIndex int       `json:"iteration_index"`
	Model          string    `json:"model"`
	InputTokens    int       `json:"input_tokens"`
	OutputTokens   int       `json:"output_tokens"`
	ToolCallCount  int       `json:"tool_call_count"`
	ToolCallIDs    []string  `json:"tool_call_ids,omitempty"`
	ToolsOffered   []string  `json:"tools_offered,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	DurationMs     int64     `json:"duration_ms"`
	HasToolCalls   bool      `json:"has_tool_calls"`
	BreakReason    string    `json:"break_reason,omitempty"`
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
	NoContext        bool          // if true, return matches only (no surrounding context)
}

// NewArchiveStore creates a new archive store at the given database path.
// Pass nil for cfg to use DefaultArchiveConfig().
// Pass nil for logger to suppress startup logging.
//
// When messagesDB is non-nil (unified mode), message queries use that
// connection against the "messages" table. When nil (legacy mode), message
// queries use the archive database's "archive_messages" table.
func NewArchiveStore(dbPath string, messagesDB *sql.DB, cfg *ArchiveConfig, logger *slog.Logger) (*ArchiveStore, error) {
	if cfg == nil {
		defaults := DefaultArchiveConfig()
		cfg = &defaults
	}

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open archive database: %w", err)
	}

	s := &ArchiveStore{
		db:                      db,
		logger:                  logger,
		messagesDB:              messagesDB,
		defaultSilenceThreshold: cfg.SilenceThreshold,
		defaultMaxMessages:      cfg.MaxContextMessages,
		defaultMaxDuration:      cfg.MaxContextDuration,
	}

	// Set storage routing based on mode.
	if messagesDB != nil {
		s.msgTableName = "messages"
		s.msgFTSName = "messages_fts"
		s.tcTableName = "tool_calls"
	} else {
		s.msgTableName = "archive_messages"
		s.msgFTSName = "archive_fts"
		s.tcTableName = "archive_tool_calls"
	}

	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("archive migrate: %w", err)
	}

	// Apply incremental migrations for existing databases
	s.migrateSchema()

	// Try to enable FTS5 — gracefully degrade if not available
	s.ftsEnabled = s.tryEnableFTS()

	if logger != nil {
		if s.ftsEnabled {
			logger.Info("session archive initialized",
				"path", dbPath,
				"fts5", true,
				"unified", messagesDB != nil,
				"silence_threshold", cfg.SilenceThreshold.String(),
				"max_context_messages", cfg.MaxContextMessages,
				"max_context_duration", cfg.MaxContextDuration.String(),
			)
		} else {
			logger.Warn("session archive: FTS5 not available — search will use slower LIKE fallback. "+
				"Rebuild SQLite with FTS5 enabled for full-text search capability.",
				"path", dbPath,
				"fts5", false,
				"unified", messagesDB != nil,
				"silence_threshold", cfg.SilenceThreshold.String(),
				"max_context_messages", cfg.MaxContextMessages,
				"max_context_duration", cfg.MaxContextDuration.String(),
			)
		}
	}

	return s, nil
}

// FTSEnabled returns whether FTS5 full-text search is available.
func (s *ArchiveStore) FTSEnabled() bool {
	return s.ftsEnabled
}

// DB returns the underlying database connection. This allows other
// stores (e.g. WorkingMemoryStore) to share the archive database
// without opening a separate connection.
func (s *ArchiveStore) DB() *sql.DB {
	return s.db
}

// Close closes the underlying database connection.
func (s *ArchiveStore) Close() error {
	return s.db.Close()
}

// msgDB returns the database connection to use for message queries.
func (s *ArchiveStore) msgDB() *sql.DB {
	if s.messagesDB != nil {
		return s.messagesDB
	}
	return s.db
}

// msgSelectCols returns the SELECT column list for message queries.
// In unified mode, archived_at and archive_reason may be NULL for active
// messages, so COALESCE is used for safe scanning.
func (s *ArchiveStore) msgSelectCols() string {
	if s.messagesDB != nil {
		return `id, conversation_id, COALESCE(session_id, '') as session_id,
			role, content, timestamp, token_count, tool_calls, tool_call_id,
			COALESCE(archived_at, '') as archived_at,
			COALESCE(archive_reason, '') as archive_reason`
	}
	return `id, conversation_id, session_id, role, content, timestamp,
		token_count, tool_calls, tool_call_id, archived_at, archive_reason`
}

// countSessionMessages returns the message count for a session from the
// appropriate messages table. Used to populate MessageCount on Session
// structs without a correlated subquery that would fail across databases.
func (s *ArchiveStore) countSessionMessages(sessionID string) int {
	var count int
	_ = s.msgDB().QueryRow(
		fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE session_id = ?`, s.msgTableName),
		sessionID,
	).Scan(&count)
	return count
}

// populateMessageCounts fills the MessageCount field on a slice of sessions
// using a single grouped query to avoid the N+1 query pattern.
func (s *ArchiveStore) populateMessageCounts(sessions []*Session) {
	if len(sessions) == 0 {
		return
	}

	ids := make([]string, 0, len(sessions))
	for _, sess := range sessions {
		if sess != nil {
			ids = append(ids, sess.ID)
		}
	}
	if len(ids) == 0 {
		return
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(
		`SELECT session_id, COUNT(*) FROM %s WHERE session_id IN (%s) GROUP BY session_id`,
		s.msgTableName,
		strings.Join(placeholders, ","),
	)

	rows, err := s.msgDB().Query(query, args...)
	if err != nil {
		return
	}
	defer rows.Close()

	counts := make(map[string]int, len(ids))
	for rows.Next() {
		var sessionID string
		var count int
		if err := rows.Scan(&sessionID, &count); err != nil {
			return
		}
		counts[sessionID] = count
	}

	for _, sess := range sessions {
		if sess == nil {
			continue
		}
		if c, ok := counts[sess.ID]; ok {
			sess.MessageCount = c
		}
	}
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

		-- Archived tool call records
		CREATE TABLE IF NOT EXISTS archive_tool_calls (
			id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			tool_name TEXT NOT NULL,
			arguments TEXT NOT NULL,
			result TEXT,
			error TEXT,
			started_at TIMESTAMP NOT NULL,
			completed_at TIMESTAMP,
			duration_ms INTEGER,
			archived_at TIMESTAMP NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_archive_tc_conversation
			ON archive_tool_calls(conversation_id, started_at);
		CREATE INDEX IF NOT EXISTS idx_archive_tc_session
			ON archive_tool_calls(session_id, started_at);
		CREATE INDEX IF NOT EXISTS idx_archive_tc_tool
			ON archive_tool_calls(tool_name);

		-- Iteration records per agent/delegate loop pass
		CREATE TABLE IF NOT EXISTS archive_iterations (
			session_id TEXT NOT NULL,
			iteration_index INTEGER NOT NULL,
			model TEXT NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			tool_call_count INTEGER NOT NULL DEFAULT 0,
			tool_call_ids TEXT,
			tools_offered TEXT,
			started_at TIMESTAMP NOT NULL,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			has_tool_calls BOOLEAN NOT NULL DEFAULT FALSE,
			break_reason TEXT,
			PRIMARY KEY (session_id, iteration_index)
		);

		CREATE INDEX IF NOT EXISTS idx_archive_iter_session
			ON archive_iterations(session_id, iteration_index);

		-- Session boundaries
		CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL,
			started_at TIMESTAMP NOT NULL,
			ended_at TIMESTAMP,
			end_reason TEXT,
			message_count INTEGER DEFAULT 0,
			summary TEXT,
			title TEXT,
			tags TEXT,
			metadata TEXT
		);

		CREATE INDEX IF NOT EXISTS idx_sessions_conversation 
			ON sessions(conversation_id, started_at DESC);
		CREATE INDEX IF NOT EXISTS idx_sessions_started 
			ON sessions(started_at DESC);

		-- Import tracking for idempotent re-imports and purge support.
		-- Maps external source IDs to archive session IDs so we can
		-- detect duplicates and cleanly remove all imported data.
		CREATE TABLE IF NOT EXISTS import_metadata (
			source_id TEXT NOT NULL,
			source_type TEXT NOT NULL,
			archive_session_id TEXT NOT NULL,
			imported_at TIMESTAMP NOT NULL,
			PRIMARY KEY (source_id, source_type),
			FOREIGN KEY (archive_session_id) REFERENCES sessions(id)
		);
	`)
	return err
}

// migrateSchema applies incremental migrations for existing databases.
func (s *ArchiveStore) migrateSchema() {
	// v2: add title, tags, metadata columns to sessions table
	// v3: add parent_session_id, parent_tool_call_id for delegate linkage
	migrations := []struct {
		column string
		sql    string
	}{
		{"title", "ALTER TABLE sessions ADD COLUMN title TEXT"},
		{"tags", "ALTER TABLE sessions ADD COLUMN tags TEXT"},
		{"metadata", "ALTER TABLE sessions ADD COLUMN metadata TEXT"},
		{"parent_session_id", "ALTER TABLE sessions ADD COLUMN parent_session_id TEXT"},
		{"parent_tool_call_id", "ALTER TABLE sessions ADD COLUMN parent_tool_call_id TEXT"},
	}

	for _, m := range migrations {
		// Check if column exists by trying a query
		_, err := s.db.Exec("SELECT " + m.column + " FROM sessions LIMIT 0")
		if err != nil {
			// Column doesn't exist — add it
			if _, err := s.db.Exec(m.sql); err != nil {
				if s.logger != nil {
					s.logger.Warn("migration failed", "column", m.column, "error", err)
				}
			}
		}
	}

	// Index for ListChildSessions query performance.
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id, started_at)`)

	// v4: add iteration_index column to archive_tool_calls for iteration linkage.
	_, err := s.db.Exec("SELECT iteration_index FROM archive_tool_calls LIMIT 0")
	if err != nil {
		if _, err := s.db.Exec("ALTER TABLE archive_tool_calls ADD COLUMN iteration_index INTEGER"); err != nil {
			if s.logger != nil {
				s.logger.Warn("migration failed", "column", "iteration_index", "error", err)
			}
		}
	}

	// Ensure archive_iterations table exists for pre-v4 databases.
	_, _ = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS archive_iterations (
			session_id TEXT NOT NULL,
			iteration_index INTEGER NOT NULL,
			model TEXT NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			tool_call_count INTEGER NOT NULL DEFAULT 0,
			tool_call_ids TEXT,
			tools_offered TEXT,
			started_at TIMESTAMP NOT NULL,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			has_tool_calls BOOLEAN NOT NULL DEFAULT FALSE,
			break_reason TEXT,
			PRIMARY KEY (session_id, iteration_index)
		)
	`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_archive_iter_session ON archive_iterations(session_id, iteration_index)`)

	// Add tool_call_ids column for existing archive_iterations tables.
	_, err = s.db.Exec("SELECT tool_call_ids FROM archive_iterations LIMIT 0")
	if err != nil {
		_, _ = s.db.Exec("ALTER TABLE archive_iterations ADD COLUMN tool_call_ids TEXT")
	}

	// Add tools_offered column for existing archive_iterations tables.
	_, err = s.db.Exec("SELECT tools_offered FROM archive_iterations LIMIT 0")
	if err != nil {
		_, _ = s.db.Exec("ALTER TABLE archive_iterations ADD COLUMN tools_offered TEXT")
	}
}

// tryEnableFTS attempts to create the FTS5 virtual table.
// Returns true if FTS5 is available, false otherwise.
func (s *ArchiveStore) tryEnableFTS() bool {
	ftsTable := s.msgFTSName
	contentTable := s.msgTableName
	db := s.msgDB()

	_, err := db.Exec(fmt.Sprintf(`
		CREATE VIRTUAL TABLE IF NOT EXISTS %s USING fts5(
			content,
			content=%s,
			content_rowid=rowid
		)
	`, ftsTable, contentTable))
	return err == nil
}

// ArchiveMessages copies messages to the immutable archive.
// This is the core "never throw data away" operation.
//
// In unified mode (messagesDB set), this is a no-op — messages already live
// in the unified table and are archived via status UPDATE by SQLiteStore.
func (s *ArchiveStore) ArchiveMessages(messages []ArchivedMessage) error {
	if s.messagesDB != nil {
		return nil // Unified mode: archival is a status UPDATE, not a cross-DB copy.
	}
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
			id, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("generate UUID: %w", err)
			}
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

// ArchiveToolCalls copies tool call records to the immutable archive.
//
// In unified mode (messagesDB set), this is a no-op — tool call archival
// is handled via status UPDATE by SQLiteStore.ArchiveToolCalls.
func (s *ArchiveStore) ArchiveToolCalls(calls []ArchivedToolCall) error {
	if s.messagesDB != nil {
		return nil // Unified mode: archival is a status UPDATE, not a cross-DB copy.
	}
	if len(calls) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO archive_tool_calls
			(id, conversation_id, session_id, tool_name, arguments,
			 result, error, started_at, completed_at, duration_ms, archived_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC()
	for _, tc := range calls {
		var completedAt any
		if tc.CompletedAt != nil {
			completedAt = tc.CompletedAt.Format(time.RFC3339Nano)
		}

		_, err := stmt.Exec(
			tc.ID, tc.ConversationID, tc.SessionID, tc.ToolName, tc.Arguments,
			nullString(tc.Result), nullString(tc.Error),
			tc.StartedAt.Format(time.RFC3339Nano), completedAt,
			tc.DurationMs, now.Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("insert tool call %s: %w", tc.ID, err)
		}
	}

	return tx.Commit()
}

// GetSessionToolCalls returns archived tool calls for a session in chronological order.
func (s *ArchiveStore) GetSessionToolCalls(sessionID string) ([]ArchivedToolCall, error) {
	rows, err := s.msgDB().Query(fmt.Sprintf(`
		SELECT id, conversation_id, session_id, tool_name, arguments,
		       result, error, started_at, completed_at, duration_ms,
		       COALESCE(archived_at, '') as archived_at,
		       iteration_index
		FROM %s
		WHERE session_id = ?
		ORDER BY started_at ASC
	`, s.tcTableName), sessionID)
	if err != nil {
		return nil, fmt.Errorf("get tool calls: %w", err)
	}
	defer rows.Close()

	return s.scanToolCalls(rows)
}

func (s *ArchiveStore) scanToolCalls(rows *sql.Rows) ([]ArchivedToolCall, error) {
	var calls []ArchivedToolCall
	for rows.Next() {
		var tc ArchivedToolCall
		var startStr, archivedStr string
		var completedStr, result, errMsg sql.NullString
		var durationMs, iterIdx sql.NullInt64

		err := rows.Scan(
			&tc.ID, &tc.ConversationID, &tc.SessionID, &tc.ToolName, &tc.Arguments,
			&result, &errMsg, &startStr, &completedStr, &durationMs, &archivedStr,
			&iterIdx,
		)
		if err != nil {
			return nil, fmt.Errorf("scan tool call: %w", err)
		}

		tc.StartedAt, _ = time.Parse(time.RFC3339Nano, startStr)
		tc.ArchivedAt, _ = time.Parse(time.RFC3339Nano, archivedStr)
		if completedStr.Valid {
			t, _ := time.Parse(time.RFC3339Nano, completedStr.String)
			tc.CompletedAt = &t
		}
		if result.Valid {
			tc.Result = result.String
		}
		if errMsg.Valid {
			tc.Error = errMsg.String
		}
		if durationMs.Valid {
			tc.DurationMs = durationMs.Int64
		}
		if iterIdx.Valid {
			idx := int(iterIdx.Int64)
			tc.IterationIndex = &idx
		}

		calls = append(calls, tc)
	}
	return calls, nil
}

// ArchiveIterations copies iteration records to the immutable archive.
// Iteration indices are automatically offset so that sessions spanning
// multiple Run() calls never collide on the (session_id, iteration_index)
// primary key.
func (s *ArchiveStore) ArchiveIterations(iterations []ArchivedIteration) error {
	if len(iterations) == 0 {
		return nil
	}

	sessionID := iterations[0].SessionID

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Determine offset: continue from the highest existing index + 1.
	var maxIdx int
	err = tx.QueryRow(
		`SELECT COALESCE(MAX(iteration_index), -1) FROM archive_iterations WHERE session_id = ?`,
		sessionID,
	).Scan(&maxIdx)
	if err != nil {
		return fmt.Errorf("query max iteration index: %w", err)
	}
	offset := maxIdx + 1

	stmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO archive_iterations
			(session_id, iteration_index, model, input_tokens, output_tokens,
			 tool_call_count, tool_call_ids, tools_offered, started_at, duration_ms, has_tool_calls, break_reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for i := range iterations {
		iterations[i].IterationIndex += offset

		var toolCallIDsJSON any
		if len(iterations[i].ToolCallIDs) > 0 {
			b, _ := json.Marshal(iterations[i].ToolCallIDs)
			toolCallIDsJSON = string(b)
		}

		var toolsOfferedJSON any
		if len(iterations[i].ToolsOffered) > 0 {
			b, _ := json.Marshal(iterations[i].ToolsOffered)
			toolsOfferedJSON = string(b)
		}

		_, err := stmt.Exec(
			iterations[i].SessionID, iterations[i].IterationIndex, iterations[i].Model,
			iterations[i].InputTokens, iterations[i].OutputTokens, iterations[i].ToolCallCount,
			toolCallIDsJSON, toolsOfferedJSON,
			iterations[i].StartedAt.Format(time.RFC3339Nano), iterations[i].DurationMs,
			iterations[i].HasToolCalls, nullString(iterations[i].BreakReason),
		)
		if err != nil {
			return fmt.Errorf("insert iteration %d: %w", iterations[i].IterationIndex, err)
		}
	}

	return tx.Commit()
}

// GetSessionIterations returns archived iterations for a session ordered
// by iteration index.
func (s *ArchiveStore) GetSessionIterations(sessionID string) ([]ArchivedIteration, error) {
	rows, err := s.db.Query(`
		SELECT session_id, iteration_index, model, input_tokens, output_tokens,
		       tool_call_count, tool_call_ids, tools_offered,
		       started_at, duration_ms, has_tool_calls, break_reason
		FROM archive_iterations
		WHERE session_id = ?
		ORDER BY iteration_index ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get iterations: %w", err)
	}
	defer rows.Close()

	var iters []ArchivedIteration
	for rows.Next() {
		var iter ArchivedIteration
		var startStr string
		var breakReason sql.NullString
		var toolCallIDsJSON sql.NullString
		var toolsOfferedJSON sql.NullString

		err := rows.Scan(
			&iter.SessionID, &iter.IterationIndex, &iter.Model,
			&iter.InputTokens, &iter.OutputTokens, &iter.ToolCallCount,
			&toolCallIDsJSON, &toolsOfferedJSON,
			&startStr, &iter.DurationMs, &iter.HasToolCalls, &breakReason,
		)
		if err != nil {
			return nil, fmt.Errorf("scan iteration: %w", err)
		}

		iter.StartedAt, _ = time.Parse(time.RFC3339Nano, startStr)
		if breakReason.Valid {
			iter.BreakReason = breakReason.String
		}
		if toolCallIDsJSON.Valid {
			_ = json.Unmarshal([]byte(toolCallIDsJSON.String), &iter.ToolCallIDs)
		}
		if toolsOfferedJSON.Valid {
			_ = json.Unmarshal([]byte(toolsOfferedJSON.String), &iter.ToolsOffered)
		}

		iters = append(iters, iter)
	}
	return iters, rows.Err()
}

// LinkToolCallsToIteration sets the iteration_index on tool calls that
// belong to a specific iteration within a session. In unified mode, this
// updates the working DB's tool_calls table.
func (s *ArchiveStore) LinkToolCallsToIteration(sessionID string, iterationIndex int, toolCallIDs []string) error {
	if len(toolCallIDs) == 0 {
		return nil
	}

	db := s.msgDB() // tool calls live in the same DB as messages
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(fmt.Sprintf(`
		UPDATE %s SET iteration_index = ?
		WHERE id = ? AND session_id = ?
	`, s.tcTableName))
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for _, tcID := range toolCallIDs {
		if _, err := stmt.Exec(iterationIndex, tcID, sessionID); err != nil {
			return fmt.Errorf("link tool call %s: %w", tcID, err)
		}
	}

	return tx.Commit()
}

// LinkPendingIterationToolCalls reads iterations for a session, and for
// each iteration with stored tool_call_ids, updates the corresponding
// archive_tool_calls rows. Call this after tool calls have been archived
// so the UPDATE finds matching rows.
func (s *ArchiveStore) LinkPendingIterationToolCalls(sessionID string) error {
	iters, err := s.GetSessionIterations(sessionID)
	if err != nil {
		return fmt.Errorf("get iterations for linking: %w", err)
	}
	for _, iter := range iters {
		if len(iter.ToolCallIDs) > 0 {
			if err := s.LinkToolCallsToIteration(sessionID, iter.IterationIndex, iter.ToolCallIDs); err != nil {
				return fmt.Errorf("link iteration %d: %w", iter.IterationIndex, err)
			}
		}
	}
	return nil
}

// Search performs a full-text search with gap-aware context expansion.
func (s *ArchiveStore) Search(opts SearchOptions) ([]SearchResult, error) {
	if strings.TrimSpace(opts.Query) == "" {
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

	ftsTable := s.msgFTSName
	msgTable := s.msgTableName
	cols := s.msgSelectCols()

	if s.ftsEnabled {
		query = fmt.Sprintf(`
			SELECT %s,
			       snippet(%s, 0, '**', '**', '...', 64) as highlight
			FROM %s
			JOIN %s am ON %s.rowid = am.rowid
		`, "am.id, am.conversation_id, COALESCE(am.session_id, '') as session_id, am.role, am.content, am.timestamp, am.token_count, am.tool_calls, am.tool_call_id, COALESCE(am.archived_at, '') as archived_at, COALESCE(am.archive_reason, '') as archive_reason",
			ftsTable, ftsTable, msgTable, ftsTable)
		// Sanitize query for FTS5: wrap each term in double quotes to prevent
		// syntax errors from special characters (periods, colons, etc.)
		sanitized := sanitizeFTS5Query(opts.Query)
		args = []any{sanitized}
		conditions := []string{ftsTable + " MATCH ?"}

		if opts.ConversationID != "" {
			conditions = append(conditions, "am.conversation_id = ?")
			args = append(args, opts.ConversationID)
		}

		query += " WHERE " + strings.Join(conditions, " AND ")
		query += " ORDER BY rank LIMIT ?"
		args = append(args, opts.Limit)
	} else {
		// LIKE fallback — less precise but functional
		query = fmt.Sprintf(`
			SELECT %s,
			       '' as highlight
			FROM %s
		`, cols, msgTable)
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

	rows, err := s.msgDB().Query(query, args...)
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
		var before, after []ArchivedMessage
		if !opts.NoContext {
			before = s.expandContext(mh.msg.ConversationID, mh.msg.Timestamp, true, opts)
			after = s.expandContext(mh.msg.ConversationID, mh.msg.Timestamp, false, opts)
		}

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

	cols := s.msgSelectCols()
	table := s.msgTableName

	if backward {
		boundary = from.Add(-opts.MaxDuration)
		query = fmt.Sprintf(`
			SELECT %s
			FROM %s
			WHERE conversation_id = ? AND timestamp < ? AND timestamp > ?
			ORDER BY timestamp DESC
			LIMIT ?
		`, cols, table)
	} else {
		boundary = from.Add(opts.MaxDuration)
		query = fmt.Sprintf(`
			SELECT %s
			FROM %s
			WHERE conversation_id = ? AND timestamp > ? AND timestamp < ?
			ORDER BY timestamp ASC
			LIMIT ?
		`, cols, table)
	}

	fromStr := from.Format(time.RFC3339Nano)
	boundaryStr := boundary.Format(time.RFC3339Nano)

	rows, err := s.msgDB().Query(query, conversationID, fromStr, boundaryStr, opts.MaxMessages)
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

// StartSession creates a new session record with the current time.
func (s *ArchiveStore) StartSession(conversationID string) (*Session, error) {
	return s.StartSessionAt(conversationID, time.Now().UTC())
}

// StartSessionAt creates a new session record with a specific start time.
// Use for imports where the original timestamp must be preserved.
func (s *ArchiveStore) StartSessionAt(conversationID string, startedAt time.Time) (*Session, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate UUID: %w", err)
	}

	sess := &Session{
		ID:             id.String(),
		ConversationID: conversationID,
		StartedAt:      startedAt,
	}

	if _, err = s.db.Exec(`
		INSERT INTO sessions (id, conversation_id, started_at, message_count)
		VALUES (?, ?, ?, 0)
	`, sess.ID, conversationID, startedAt.Format(time.RFC3339Nano)); err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}

	return sess, nil
}

// StartSessionWithOptions creates a new session record with optional
// parent linkage. Use [WithParentSession] and [WithParentToolCall] to
// set parent fields for delegate sessions.
func (s *ArchiveStore) StartSessionWithOptions(conversationID string, opts ...SessionOption) (*Session, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate UUID: %w", err)
	}

	sess := &Session{
		ID:             id.String(),
		ConversationID: conversationID,
		StartedAt:      time.Now().UTC(),
	}
	for _, opt := range opts {
		opt(sess)
	}

	if _, err = s.db.Exec(`
		INSERT INTO sessions (id, conversation_id, started_at, message_count, parent_session_id, parent_tool_call_id)
		VALUES (?, ?, ?, 0, ?, ?)
	`, sess.ID, conversationID, sess.StartedAt.Format(time.RFC3339Nano),
		nullString(sess.ParentSessionID), nullString(sess.ParentToolCallID)); err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}

	return sess, nil
}

// EndSession marks a session as ended at the current time.
func (s *ArchiveStore) EndSession(sessionID string, reason string) error {
	return s.EndSessionAt(sessionID, reason, time.Now().UTC())
}

// EndSessionAt marks a session as ended at a specific time.
func (s *ArchiveStore) EndSessionAt(sessionID string, reason string, endedAt time.Time) error {
	_, err := s.db.Exec(`
		UPDATE sessions SET ended_at = ?, end_reason = ? WHERE id = ?
	`, endedAt.Format(time.RFC3339Nano), reason, sessionID)
	return err
}

// CloseOrphanedSessions ends any sessions that are still open (ended_at IS NULL)
// but were started before the given cutoff time. This recovers sessions orphaned
// by crashes (SIGKILL, OOM, panics) where EndSession was never called. Returns
// the number of sessions closed.
func (s *ArchiveStore) CloseOrphanedSessions(before time.Time) (int64, error) {
	result, err := s.db.Exec(`
		UPDATE sessions
		SET ended_at = ?, end_reason = 'crash_recovery'
		WHERE ended_at IS NULL AND started_at < ?
	`, time.Now().UTC().Format(time.RFC3339Nano), before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("close orphaned sessions: %w", err)
	}
	return result.RowsAffected()
}

// SetSessionSummary updates only the summary text for a session.
// For richer metadata, use SetSessionMetadata.
func (s *ArchiveStore) SetSessionSummary(sessionID string, summary string) error {
	_, err := s.db.Exec(`
		UPDATE sessions SET summary = ? WHERE id = ?
	`, summary, sessionID)
	return err
}

// SetSessionMetadata updates the full rich metadata for a session,
// including title, tags, summary, and structured metadata JSON.
func (s *ArchiveStore) SetSessionMetadata(sessionID string, meta *SessionMetadata, title string, tags []string) error {
	var metaJSON []byte
	if meta != nil {
		var err error
		metaJSON, err = json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("marshal metadata: %w", err)
		}
	}

	var tagsJSON []byte
	if len(tags) > 0 {
		var err error
		tagsJSON, err = json.Marshal(tags)
		if err != nil {
			return fmt.Errorf("marshal tags: %w", err)
		}
	}

	// Update summary from the metadata's paragraph-level summary
	summary := ""
	if meta != nil {
		summary = meta.Paragraph
		if summary == "" {
			summary = meta.OneLiner
		}
	}

	_, err := s.db.Exec(`
		UPDATE sessions SET title = ?, tags = ?, metadata = ?, summary = ?
		WHERE id = ?
	`, nullString(title), nullString(string(tagsJSON)), nullString(string(metaJSON)), nullString(summary), sessionID)
	return err
}

// ActiveSession returns the most recent unclosed session for a conversation, if any.
func (s *ArchiveStore) ActiveSession(conversationID string) (*Session, error) {
	row := s.db.QueryRow(`
		SELECT id, conversation_id, started_at, ended_at, end_reason,
		       0 AS message_count,
		       summary, title, tags, metadata, parent_session_id, parent_tool_call_id
		FROM sessions
		WHERE conversation_id = ? AND ended_at IS NULL
		ORDER BY started_at DESC
		LIMIT 1
	`, conversationID)

	sess, err := s.scanSession(row)
	if sess != nil {
		sess.MessageCount = s.countSessionMessages(sess.ID)
	}
	return sess, err
}

// GetSession retrieves a session by ID.
func (s *ArchiveStore) GetSession(sessionID string) (*Session, error) {
	row := s.db.QueryRow(`
		SELECT id, conversation_id, started_at, ended_at, end_reason,
		       0 AS message_count,
		       summary, title, tags, metadata, parent_session_id, parent_tool_call_id
		FROM sessions WHERE id = ?
	`, sessionID)

	sess, err := s.scanSession(row)
	if sess != nil {
		sess.MessageCount = s.countSessionMessages(sess.ID)
	}
	return sess, err
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
			SELECT id, conversation_id, started_at, ended_at, end_reason,
			       0 AS message_count,
			       summary, title, tags, metadata, parent_session_id, parent_tool_call_id
			FROM sessions WHERE conversation_id = ?
			ORDER BY started_at DESC LIMIT ?
		`
		args = []any{conversationID, limit}
	} else {
		query = `
			SELECT id, conversation_id, started_at, ended_at, end_reason,
			       0 AS message_count,
			       summary, title, tags, metadata, parent_session_id, parent_tool_call_id
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
	if err := rows.Err(); err != nil {
		return nil, err
	}

	s.populateMessageCounts(sessions)
	return sessions, nil
}

// ListChildSessions returns sessions whose parent_session_id matches
// the given ID, ordered chronologically. Used by the session inspector
// to show delegate sub-sessions.
func (s *ArchiveStore) ListChildSessions(parentSessionID string) ([]*Session, error) {
	rows, err := s.db.Query(`
		SELECT id, conversation_id, started_at, ended_at, end_reason,
		       0 AS message_count,
		       summary, title, tags, metadata, parent_session_id, parent_tool_call_id
		FROM sessions WHERE parent_session_id = ?
		ORDER BY started_at ASC
	`, parentSessionID)
	if err != nil {
		return nil, fmt.Errorf("list child sessions: %w", err)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}

	s.populateMessageCounts(sessions)
	return sessions, nil
}

// UnsummarizedSessions returns ended sessions that have no metadata yet,
// ordered oldest-first for catch-up processing. Only sessions with at
// least one message are returned — the message count is checked via a
// post-query filter because messages may live in a different database
// than sessions in unified mode.
func (s *ArchiveStore) UnsummarizedSessions(limit int) ([]*Session, error) {
	if limit <= 0 {
		limit = 50
	}

	// Over-fetch candidates, then filter by message count. This avoids a
	// cross-DB EXISTS subquery that would fail in unified mode.
	rows, err := s.db.Query(`
		SELECT id, conversation_id, started_at, ended_at, end_reason,
		       0 AS message_count,
		       summary, title, tags, metadata,
		       parent_session_id, parent_tool_call_id
		FROM sessions
		WHERE ended_at IS NOT NULL
		  AND (title IS NULL OR title = '')
		ORDER BY ended_at ASC
		LIMIT ?
	`, limit*3) // over-fetch to account for sessions with no messages
	if err != nil {
		return nil, fmt.Errorf("unsummarized sessions: %w", err)
	}
	defer rows.Close()

	var candidates []*Session
	for rows.Next() {
		sess, err := s.scanSessionRow(rows)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Batch-fetch message counts for all candidates in one query.
	s.populateMessageCounts(candidates)

	// Filter to sessions with at least one message and apply limit.
	var sessions []*Session
	for _, sess := range candidates {
		if sess.MessageCount > 0 {
			sessions = append(sessions, sess)
			if len(sessions) >= limit {
				break
			}
		}
	}

	return sessions, nil
}

// GetSessionTranscript returns all archived messages for a session in chronological order.
func (s *ArchiveStore) GetSessionTranscript(sessionID string) ([]ArchivedMessage, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM %s
		WHERE session_id = ?
		ORDER BY timestamp ASC
	`, s.msgSelectCols(), s.msgTableName)

	rows, err := s.msgDB().Query(query, sessionID)
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

	cols := s.msgSelectCols()
	table := s.msgTableName
	var query string
	var args []any

	if conversationID != "" {
		query = fmt.Sprintf(`
			SELECT %s
			FROM %s
			WHERE conversation_id = ? AND timestamp >= ? AND timestamp <= ?
			ORDER BY timestamp ASC
			LIMIT ?
		`, cols, table)
		args = []any{conversationID, from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano), limit}
	} else {
		query = fmt.Sprintf(`
			SELECT %s
			FROM %s
			WHERE timestamp >= ? AND timestamp <= ?
			ORDER BY timestamp ASC
			LIMIT ?
		`, cols, table)
		args = []any{from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano), limit}
	}

	rows, err := s.msgDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query by time range: %w", err)
	}
	defer rows.Close()

	return s.scanMessages(rows)
}

// ExportSessionMarkdown exports a session transcript as human-readable markdown.
// Includes tool call records interleaved chronologically with messages.
func (s *ArchiveStore) ExportSessionMarkdown(sessionID string) (string, error) {
	sess, err := s.GetSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("get session: %w", err)
	}

	messages, err := s.GetSessionTranscript(sessionID)
	if err != nil {
		return "", fmt.Errorf("get transcript: %w", err)
	}

	toolCalls, _ := s.GetSessionToolCalls(sessionID)

	// Build a lookup of tool calls by start time for interleaving
	type toolCallEntry struct {
		tc   ArchivedToolCall
		used bool
	}
	tcEntries := make([]toolCallEntry, len(toolCalls))
	for i, tc := range toolCalls {
		tcEntries[i] = toolCallEntry{tc: tc}
	}

	var sb strings.Builder

	// Header
	sb.WriteString(fmt.Sprintf("# Session %s\n\n", ShortID(sessionID)))
	sb.WriteString(fmt.Sprintf("**Conversation:** %s\n", sess.ConversationID))
	sb.WriteString(fmt.Sprintf("**Started:** %s\n", sess.StartedAt.Format("2006-01-02 15:04:05 MST")))
	if sess.EndedAt != nil {
		sb.WriteString(fmt.Sprintf("**Ended:** %s (%s)\n", sess.EndedAt.Format("2006-01-02 15:04:05 MST"), sess.EndReason))
	}
	sb.WriteString(fmt.Sprintf("**Messages:** %d\n", len(messages)))
	if len(toolCalls) > 0 {
		sb.WriteString(fmt.Sprintf("**Tool Calls:** %d\n", len(toolCalls)))
	}
	sb.WriteString("\n---\n\n")

	// Messages with interleaved tool calls
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
			// Find matching tool call record for richer output
			var matchedTC *ArchivedToolCall
			for i := range tcEntries {
				if !tcEntries[i].used && tcEntries[i].tc.StartedAt.Before(m.Timestamp.Add(time.Second)) &&
					tcEntries[i].tc.StartedAt.After(m.Timestamp.Add(-30*time.Second)) {
					matchedTC = &tcEntries[i].tc
					tcEntries[i].used = true
					break
				}
			}

			if matchedTC != nil {
				duration := ""
				if matchedTC.DurationMs > 0 {
					duration = fmt.Sprintf(" (%dms)", matchedTC.DurationMs)
				}
				sb.WriteString(fmt.Sprintf("### 🔧 %s%s [%s]\n\n", matchedTC.ToolName, duration, ts))
				sb.WriteString(fmt.Sprintf("**Arguments:**\n```json\n%s\n```\n\n", matchedTC.Arguments))
				if matchedTC.Error != "" {
					sb.WriteString(fmt.Sprintf("**Error:** %s\n\n", matchedTC.Error))
				}
				sb.WriteString(fmt.Sprintf("**Result:**\n```\n%s\n```\n\n", m.Content))
			} else {
				name := m.ToolCallID
				if name == "" {
					name = "tool"
				}
				sb.WriteString(fmt.Sprintf("### 🔧 %s [%s]\n\n```\n%s\n```\n\n", name, ts, m.Content))
			}
		default:
			sb.WriteString(fmt.Sprintf("### %s [%s]\n\n%s\n\n", role, ts, m.Content))
		}
	}

	return sb.String(), nil
}

// Stats returns archive statistics.
func (s *ArchiveStore) Stats() (map[string]any, error) {
	stats := make(map[string]any)

	msgDB := s.msgDB()
	table := s.msgTableName

	var msgCount, sessionCount, toolCallCount int
	var oldestStr, newestStr sql.NullString

	_ = msgDB.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(&msgCount)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessionCount)
	_ = s.msgDB().QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, s.tcTableName)).Scan(&toolCallCount)
	_ = msgDB.QueryRow(fmt.Sprintf(`SELECT MIN(timestamp) FROM %s`, table)).Scan(&oldestStr)
	_ = msgDB.QueryRow(fmt.Sprintf(`SELECT MAX(timestamp) FROM %s`, table)).Scan(&newestStr)

	stats["total_messages"] = msgCount
	stats["total_sessions"] = sessionCount
	stats["total_tool_calls"] = toolCallCount

	if oldestStr.Valid {
		stats["oldest_message"] = oldestStr.String
	}
	if newestStr.Valid {
		stats["newest_message"] = newestStr.String
	}

	// Messages by role
	byRole := make(map[string]int)
	rows, err := msgDB.Query(fmt.Sprintf(`SELECT role, COUNT(*) FROM %s GROUP BY role`, table))
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

	// Messages by reason/status
	if s.messagesDB != nil {
		// Unified mode: group by status instead of archive_reason
		byStatus := make(map[string]int)
		rows2, err := msgDB.Query(`SELECT COALESCE(status, 'unknown'), COUNT(*) FROM messages GROUP BY status`)
		if err == nil {
			defer rows2.Close()
			for rows2.Next() {
				var status string
				var count int
				if err := rows2.Scan(&status, &count); err == nil {
					byStatus[status] = count
				}
			}
		}
		stats["by_status"] = byStatus
	} else {
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
	}

	return stats, nil
}

// --- helpers ---

func (s *ArchiveStore) scanSession(row *sql.Row) (*Session, error) {
	var sess Session
	var startStr string
	var endStr, endReason, summary, title, tagsJSON, metaJSON sql.NullString
	var parentSessionID, parentToolCallID sql.NullString

	err := row.Scan(&sess.ID, &sess.ConversationID, &startStr,
		&endStr, &endReason, &sess.MessageCount, &summary,
		&title, &tagsJSON, &metaJSON, &parentSessionID, &parentToolCallID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	populateSession(&sess, startStr, endStr, endReason, summary, title, tagsJSON, metaJSON, parentSessionID, parentToolCallID, s.logger)
	return &sess, nil
}

func (s *ArchiveStore) scanSessionRow(rows *sql.Rows) (*Session, error) {
	var sess Session
	var startStr string
	var endStr, endReason, summary, title, tagsJSON, metaJSON sql.NullString
	var parentSessionID, parentToolCallID sql.NullString

	err := rows.Scan(&sess.ID, &sess.ConversationID, &startStr,
		&endStr, &endReason, &sess.MessageCount, &summary,
		&title, &tagsJSON, &metaJSON, &parentSessionID, &parentToolCallID)
	if err != nil {
		return nil, err
	}

	populateSession(&sess, startStr, endStr, endReason, summary, title, tagsJSON, metaJSON, parentSessionID, parentToolCallID, s.logger)
	return &sess, nil
}

// populateSession fills parsed fields from nullable database columns.
func populateSession(sess *Session, startStr string, endStr, endReason, summary, title, tagsJSON, metaJSON, parentSessionID, parentToolCallID sql.NullString, logger *slog.Logger) {
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
	if title.Valid {
		sess.Title = title.String
	}
	if tagsJSON.Valid {
		if err := json.Unmarshal([]byte(tagsJSON.String), &sess.Tags); err != nil && logger != nil {
			logger.Warn("corrupt tags JSON in session", "session", ShortID(sess.ID), "error", err)
		}
	}
	if metaJSON.Valid {
		var meta SessionMetadata
		if err := json.Unmarshal([]byte(metaJSON.String), &meta); err != nil {
			if logger != nil {
				logger.Warn("corrupt metadata JSON in session", "session", ShortID(sess.ID), "error", err)
			}
		} else {
			sess.Metadata = &meta
		}
	}
	if parentSessionID.Valid {
		sess.ParentSessionID = parentSessionID.String
	}
	if parentToolCallID.Valid {
		sess.ParentToolCallID = parentToolCallID.String
	}
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

// RecordImport creates a mapping from an external source ID to an archive session ID.
// Used by importers to track which external sessions have already been imported,
// enabling idempotent re-runs.
func (s *ArchiveStore) RecordImport(sourceID, sourceType, archiveSessionID string) error {
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO import_metadata (source_id, source_type, archive_session_id, imported_at)
		VALUES (?, ?, ?, ?)
	`, sourceID, sourceType, archiveSessionID, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// IsImported checks whether an external source ID has already been imported.
func (s *ArchiveStore) IsImported(sourceID, sourceType string) (bool, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM import_metadata WHERE source_id = ? AND source_type = ?
	`, sourceID, sourceType).Scan(&count)
	return count > 0, err
}

// PurgeImported removes all archive data that was imported from a given source type.
// This deletes sessions, messages, tool calls, and import metadata — a clean slate
// so the import can be re-run with improved logic.
//
// In unified mode, messages live in a different database than sessions and metadata.
// The method handles this by deleting messages from the messages DB first, then
// cleaning up sessions and metadata from the archive DB in a transaction.
func (s *ArchiveStore) PurgeImported(sourceType string) (int, error) {
	// Query session IDs outside a transaction so we can use them across DBs.
	rows, err := s.db.Query(`
		SELECT archive_session_id FROM import_metadata WHERE source_type = ?
	`, sourceType)
	if err != nil {
		return 0, fmt.Errorf("query imports: %w", err)
	}

	var sessionIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan session id: %w", err)
		}
		sessionIDs = append(sessionIDs, id)
	}
	rows.Close()

	if len(sessionIDs) == 0 {
		return 0, nil
	}

	// In unified mode, messages and tool calls live in s.messagesDB — delete
	// them there first, outside the archive.db transaction.
	// FTS triggers (if present) handle the FTS cleanup automatically on DELETE.
	if s.messagesDB != nil {
		wdb := s.msgDB()
		for _, sid := range sessionIDs {
			if _, err := wdb.Exec(fmt.Sprintf(`DELETE FROM %s WHERE session_id = ?`, s.msgTableName), sid); err != nil {
				return 0, fmt.Errorf("delete messages for session %s: %w", ShortID(sid), err)
			}
			if _, err := wdb.Exec(fmt.Sprintf(`DELETE FROM %s WHERE session_id = ?`, s.tcTableName), sid); err != nil {
				return 0, fmt.Errorf("delete tool calls for session %s: %w", ShortID(sid), err)
			}
		}
	}

	// Delete sessions and metadata from archive.db.
	// In legacy mode, messages and tool calls also live here.
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, sid := range sessionIDs {
		// In legacy mode (messagesDB == nil), messages and tool calls are in archive.db.
		if s.messagesDB == nil {
			if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE session_id = ?`, s.msgTableName), sid); err != nil {
				return 0, fmt.Errorf("delete messages for session %s: %w", ShortID(sid), err)
			}
			if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE session_id = ?`, s.tcTableName), sid); err != nil {
				return 0, fmt.Errorf("delete tool calls for session %s: %w", ShortID(sid), err)
			}
		}
		if _, err := tx.Exec(`DELETE FROM sessions WHERE id = ?`, sid); err != nil {
			return 0, fmt.Errorf("delete session %s: %w", ShortID(sid), err)
		}
	}

	// In legacy mode, rebuild FTS since we deleted directly (no triggers).
	if s.messagesDB == nil && s.ftsEnabled {
		ftsTable := s.msgFTSName
		if _, err := tx.Exec(fmt.Sprintf(`INSERT INTO %s(%s) VALUES('rebuild')`, ftsTable, ftsTable)); err != nil {
			return 0, fmt.Errorf("rebuild FTS: %w", err)
		}
	}

	// Remove all import metadata for this source type.
	if _, err := tx.Exec(`DELETE FROM import_metadata WHERE source_type = ?`, sourceType); err != nil {
		return 0, fmt.Errorf("delete import metadata: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit purge: %w", err)
	}

	return len(sessionIDs), nil
}

// ShortID safely truncates an ID to 8 characters for display.
// Returns the full string if shorter than 8 characters.
func ShortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// sanitizeFTS5Query wraps each search term in double quotes to prevent FTS5
// syntax errors from special characters like periods, colons, and parentheses,
// then joins terms with OR so that broader recall is possible. BM25 ranking
// ensures messages matching more terms score higher.
//
// E.g., "consciousness hard problem" becomes `"consciousness" OR "hard" OR "problem"`.
func sanitizeFTS5Query(query string) string {
	words := strings.Fields(query)
	if len(words) == 0 {
		return ""
	}
	quoted := make([]string, len(words))
	for i, w := range words {
		// Escape any existing double quotes in the term.
		w = strings.ReplaceAll(w, `"`, `""`)
		quoted[i] = `"` + w + `"`
	}
	return strings.Join(quoted, " OR ")
}
