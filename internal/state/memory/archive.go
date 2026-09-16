package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
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

	// Whether FTS5 is available
	ftsEnabled bool

	// Whether sessions_fts was set up successfully. Separate from
	// ftsEnabled because trySetupSessionsFTS can fail independently of
	// the core FTS5 availability probe (e.g. corrupted pre-existing
	// virtual table, permissions issue on a shadow table). SearchSessions
	// gates on this so a failed setup degrades to "no hits" rather than
	// erroring at query time against a missing/broken FTS table.
	sessionsFTSEnabled bool

	// sessionCloseCallback runs after EndSession / EndSessionAt commits.
	// Used by the app wiring to enqueue archivist work for the just-
	// closed session — see issues #989, #1024. The callback runs synchronously
	// in the calling goroutine; implementations MUST be fast and
	// non-blocking (e.g. enqueue to a channel, return immediately).
	// Errors are swallowed by the caller: closing a session is the
	// authoritative state change; downstream notification is best-effort
	// and the summarizer's periodic scan is the backstop for missed
	// deliveries.
	sessionCloseCallback func(sessionID, reason string)

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
// session (e.g. the thane_now or thane_assign call in the parent).
func WithParentToolCall(id string) SessionOption {
	return func(s *Session) { s.ParentToolCallID = id }
}

// WithChannelBinding snapshots the effective conversation/channel
// binding onto the archived session at creation time.
func WithChannelBinding(binding *ChannelBinding) SessionOption {
	return func(s *Session) {
		if binding == nil {
			return
		}
		if s.Metadata == nil {
			s.Metadata = &SessionMetadata{}
		}
		s.Metadata.ChannelBinding = binding.Clone()
	}
}

// SessionMetadata holds rich, LLM-generated metadata for human-oriented
// search and browsing. Stored as JSON in the database for flexibility —
// new fields can be added without schema migrations.
type SessionMetadata struct {
	// ChannelBinding is the channel/contact binding snapshot active when
	// the session was created. This preserves the security-policy
	// identity view Thane had at the time for later forensics.
	ChannelBinding *ChannelBinding `json:"channel_binding,omitempty"`

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

	// Legacy delegation execution details, preserved from the delegations
	// table migration (#446). Only populated for imported delegation records.
	Delegation *DelegationMetadata `json:"delegation,omitempty"`
}

// DelegationMetadata holds delegation-specific fields preserved from the
// legacy delegations table during migration (#446).
type DelegationMetadata struct {
	Task          string `json:"task"`
	Guidance      string `json:"guidance,omitempty"`
	Profile       string `json:"profile"`
	Model         string `json:"model"`
	Iterations    int    `json:"iterations"`
	MaxIterations int    `json:"max_iterations"`
	InputTokens   int    `json:"input_tokens"`
	OutputTokens  int    `json:"output_tokens"`
	Exhausted     bool   `json:"exhausted"`
	ExhaustReason string `json:"exhaust_reason,omitempty"`
	ResultContent string `json:"result_content,omitempty"`
	DurationMs    int64  `json:"duration_ms"`
	Error         string `json:"error,omitempty"`

	// Messages is the raw JSON-serialized conversation history from the
	// legacy delegations table. Preserved to avoid data loss — these
	// messages predate the per-message storage in the messages table.
	Messages string `json:"messages,omitempty"`
}

// IdleSessionInfo holds an active session's identity and last activity time
// for idle timeout evaluation by the summarizer worker.
type IdleSessionInfo struct {
	SessionID      string
	ConversationID string
	LastActivity   time.Time
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
	Match         Message   `json:"match"`
	SessionID     string    `json:"session_id"`
	ContextBefore []Message `json:"context_before"`
	ContextAfter  []Message `json:"context_after"`
	Highlight     string    `json:"highlight,omitempty"`
	// Score is the relevance signal (higher is a better match). It is
	// derived from the FTS5 BM25 rank, negated so larger means more
	// relevant. Comparable within a MatchType bucket but not across
	// buckets (phrase and terms passes score against different MATCH
	// expressions). Zero on the LIKE fallback path.
	Score float64 `json:"score"`
	// MatchType records which pass produced the hit: "phrase" (the
	// literal-phrase precision pass) or "terms" (the OR-of-terms
	// recall backfill). Empty on the LIKE fallback path.
	MatchType string `json:"match_type,omitempty"`
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

	// From / To optionally scope the raw-message search to a time
	// window (inclusive). Zero values mean unbounded on that edge.
	// Only raw-message search honors these; distilled session and
	// working-memory surfaces stay unscoped in time. ConversationID
	// scopes every surface.
	From time.Time
	To   time.Time

	// IncludeAnticipations controls whether wake-bridge synthetic
	// "Anticipation matched: …" rows can appear in results. The
	// default (false) drops them because their dense-keyword shape
	// dominates BM25 ranking for any household-vocabulary query
	// (the entity name often repeats 5-10 times in one event),
	// drowning out the conversational hits the model is actually
	// reaching for. Set this true when an operator or diagnostic
	// caller explicitly wants to inspect wake events.
	IncludeAnticipations bool
}

// NewArchiveStoreFromDB creates an ArchiveStore backed by an existing database
// connection (typically the main thane.db). The store does NOT own the
// connection and Close will not close it. All session, iteration, message,
// and tool-call queries go to the shared connection. SQLiteStore must have
// initialized the message and tool-call schema first. A nil cfg uses
// DefaultArchiveConfig; a nil logger suppresses archive startup logging.
func NewArchiveStoreFromDB(db *sql.DB, cfg *ArchiveConfig, logger *slog.Logger) (*ArchiveStore, error) {
	if cfg == nil {
		defaults := DefaultArchiveConfig()
		cfg = &defaults
	}

	s := &ArchiveStore{
		db:                      db,
		logger:                  logger,
		defaultSilenceThreshold: cfg.SilenceThreshold,
		defaultMaxMessages:      cfg.MaxContextMessages,
		defaultMaxDuration:      cfg.MaxContextDuration,
	}

	if err := s.migrateSessionTables(); err != nil {
		return nil, fmt.Errorf("session table migration: %w", err)
	}

	s.migrateSchema()
	s.ftsEnabled = s.tryEnableFTS()
	s.sessionsFTSEnabled = s.trySetupSessionsFTS()

	if logger != nil {
		logger.Info("session archive initialized (consolidated)",
			"fts5", s.ftsEnabled,
		)
	}

	return s, nil
}

// migrateSessionTables creates the archive metadata tables on the shared
// database. SQLiteStore creates the message and tool-call tables.
func (s *ArchiveStore) migrateSessionTables() error {
	_, err := s.db.Exec(`
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
		-- Open sessions are the rare rows in a table that only grows;
		-- the partial index keeps every "still open?" query (active
		-- count, orphan recovery, shutdown enumeration) off the full
		-- table scan that once cost ~0.8s per ops-panel render.
		CREATE INDEX IF NOT EXISTS idx_sessions_open
			ON sessions(ended_at) WHERE ended_at IS NULL;

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

// Close is a no-op. The caller owns the shared database connection and must
// close it after every store using it has stopped.
func (s *ArchiveStore) Close() error {
	return nil
}

// msgSelectCols returns the SELECT column list for message queries.
// archived_at and archive_reason may be NULL for active
// messages, so COALESCE is used for safe scanning.
func (s *ArchiveStore) msgSelectCols() string {
	return `id, conversation_id, COALESCE(session_id, '') as session_id,
		role, content, timestamp, token_count, tool_calls, tool_call_id,
		COALESCE(archived_at, '') as archived_at,
		COALESCE(archive_reason, '') as archive_reason,
		COALESCE(origin, '') as origin`
}

// countSessionMessages populates MessageCount on individual session lookups.
func (s *ArchiveStore) countSessionMessages(sessionID string) int {
	var count int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM messages WHERE session_id = ?`,
		sessionID,
	).Scan(&count)
	return count
}

// populateMessageCounts fills the MessageCount field on a slice of sessions
// using a single grouped query to avoid the N+1 query pattern.
func (s *ArchiveStore) populateMessageCounts(sessions []*Session) error {
	if len(sessions) == 0 {
		return nil
	}

	ids := make([]string, 0, len(sessions))
	for _, sess := range sessions {
		if sess != nil {
			ids = append(ids, sess.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	placeholders, args := database.InList(ids)

	query := fmt.Sprintf(`SELECT session_id, COUNT(*) FROM messages WHERE session_id IN (%s) GROUP BY session_id`, placeholders)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return fmt.Errorf("query session message counts: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int, len(ids))
	for rows.Next() {
		var sessionID string
		var count int
		if err := rows.Scan(&sessionID, &count); err != nil {
			return fmt.Errorf("scan session message count: %w", err)
		}
		counts[sessionID] = count
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate session message counts: %w", err)
	}

	for _, sess := range sessions {
		if sess == nil {
			continue
		}
		if c, ok := counts[sess.ID]; ok {
			sess.MessageCount = c
		}
	}
	return nil
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

	// Add tool_call_ids column for existing archive_iterations tables.
	if _, err := s.db.Exec("SELECT tool_call_ids FROM archive_iterations LIMIT 0"); err != nil {
		_, _ = s.db.Exec("ALTER TABLE archive_iterations ADD COLUMN tool_call_ids TEXT")
	}

	// Add tools_offered column for existing archive_iterations tables.
	if _, err := s.db.Exec("SELECT tools_offered FROM archive_iterations LIMIT 0"); err != nil {
		_, _ = s.db.Exec("ALTER TABLE archive_iterations ADD COLUMN tools_offered TEXT")
	}

}

// tryEnableFTS attempts to create the FTS5 virtual table. Returns true if
// FTS5 is available and its sync triggers were installed
// — i.e. searchFTS can be trusted to return complete results. Returns
// false otherwise so the store falls back to the LIKE path rather than
// querying an index that cannot stay in sync.
func (s *ArchiveStore) tryEnableFTS() bool {
	db := s.db

	if _, err := db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
			content,
			content=messages,
			content_rowid=rowid
		)
	`); err != nil {
		return false
	}

	// SQLiteStore writes messages independently of the archive reader, so
	// triggers and a one-time backfill keep the external-content FTS
	// index complete, mirroring trySetupSessionsFTS.
	//
	// If the triggers cannot be installed, sync cannot be established:
	// disable FTS so Search uses the LIKE fallback instead of silently
	// querying a stale/empty index.
	if err := s.setupMessagesFTSSync(); err != nil {
		if s.logger != nil {
			s.logger.Warn("messages_fts sync unavailable; using LIKE fallback", "error", err)
		}
		return false
	}
	return true
}

// setupMessagesFTSSync installs AFTER INSERT/DELETE/UPDATE sync triggers on
// the unified messages table and backfills the external-content FTS index.
// It returns a non-nil error only when the triggers cannot be installed —
// i.e. ongoing sync cannot be established, so the caller must not trust the
// index. Backfill (rebuild) failure is non-fatal: the triggers keep the
// index growing for new writes and the next startup retries the rebuild
// against the same docsize-shortfall signal.
func (s *ArchiveStore) setupMessagesFTSSync() error {
	db := s.db
	stmts := []string{
		`
			CREATE TRIGGER IF NOT EXISTS messages_fts_ai AFTER INSERT ON messages BEGIN
				INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, COALESCE(new.content, ''));
			END
		`,
		// DELETE/UPDATE use the FTS5 'delete' command to tombstone the old
		// row — external-content tables cannot do partial-column updates.
		`
			CREATE TRIGGER IF NOT EXISTS messages_fts_ad AFTER DELETE ON messages BEGIN
				INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.rowid, COALESCE(old.content, ''));
			END
		`,
		// Re-index only when content actually changes. Messages are updated
		// frequently for status/archived_at, which leave content untouched;
		// the WHEN guard avoids needless index churn on those writes. (A
		// deliberate refinement over the sessions_fts triggers, where the
		// indexed columns change together and no guard is warranted.)
		`
			CREATE TRIGGER IF NOT EXISTS messages_fts_au AFTER UPDATE ON messages
			WHEN old.content IS NOT new.content BEGIN
				INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.rowid, COALESCE(old.content, ''));
				INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, COALESCE(new.content, ''));
			END
		`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("install messages_fts trigger: %w", err)
		}
	}

	// Idempotent backfill: rebuild when the inverted index holds fewer docs
	// than the source table — covering first creation and a prior failed
	// rebuild. Probe the _docsize shadow table, not COUNT(*) on the FTS
	// table: external-content FTS5 proxies COUNT(*) through to the source
	// table, which would always self-equal and suppress the rebuild.
	// Backfill problems below are non-fatal — triggers are already in place.
	var docCount, srcCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages_fts_docsize`).Scan(&docCount); err != nil {
		if s.logger != nil {
			s.logger.Warn("messages_fts docsize probe failed; skipping backfill", "error", err)
		}
		return nil
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&srcCount); err != nil {
		if s.logger != nil {
			s.logger.Warn("messages count probe failed; skipping backfill", "error", err)
		}
		return nil
	}
	if docCount < srcCount {
		if _, err := db.Exec(`INSERT INTO messages_fts(messages_fts) VALUES('rebuild')`); err != nil {
			if s.logger != nil {
				s.logger.Warn("messages_fts backfill failed; next startup will retry",
					"docsize", docCount, "messages", srcCount, "error", err)
			}
		}
	}
	return nil
}

// ImportMessages inserts externally sourced messages (e.g. from openclaw-import)
// into the shared messages table with status='archived'. Existing IDs are
// ignored, and FTS triggers keep search synchronized with inserted rows.
func (s *ArchiveStore) ImportMessages(messages []Message) error {
	if len(messages) == 0 {
		return nil
	}

	db := s.db
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO messages
			(id, conversation_id, session_id, role, content, timestamp,
			 token_count, tool_calls, tool_call_id,
			 status, archived_at, archive_reason, origin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'archived', ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

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

		if _, err := stmt.Exec(
			m.ID, m.ConversationID, m.SessionID, m.Role, m.Content,
			m.Timestamp.Format(time.RFC3339Nano),
			m.TokenCount, nullString(m.ToolCalls), nullString(m.ToolCallID),
			m.ArchivedAt.Format(time.RFC3339Nano), m.ArchiveReason, m.Origin,
		); err != nil {
			return fmt.Errorf("insert message %s: %w", m.ID, err)
		}
	}

	return tx.Commit()
}

// ImportToolCalls inserts externally-sourced tool calls (e.g. from
// openclaw-import) into the shared tool_calls table with status='archived'.
// Existing IDs are ignored.
func (s *ArchiveStore) ImportToolCalls(calls []ArchivedToolCall) error {
	if len(calls) == 0 {
		return nil
	}

	db := s.db
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO tool_calls
			(id, conversation_id, session_id, tool_name, arguments,
			 result, error, started_at, completed_at, duration_ms,
			 status, archived_at, iteration_index)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'archived', ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC()
	for _, tc := range calls {
		if tc.ID == "" {
			id, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("generate UUID: %w", err)
			}
			tc.ID = id.String()
		}

		archivedAt := now
		if !tc.ArchivedAt.IsZero() {
			archivedAt = tc.ArchivedAt
		}

		var completedAt any
		if tc.CompletedAt != nil {
			completedAt = tc.CompletedAt.Format(time.RFC3339Nano)
		}

		if _, err := stmt.Exec(
			tc.ID, tc.ConversationID, tc.SessionID, tc.ToolName, tc.Arguments,
			nullString(tc.Result), nullString(tc.Error),
			tc.StartedAt.Format(time.RFC3339Nano), completedAt,
			tc.DurationMs, archivedAt.Format(time.RFC3339Nano),
			tc.IterationIndex,
		); err != nil {
			return fmt.Errorf("insert tool call %s: %w", tc.ID, err)
		}
	}

	return tx.Commit()
}

// GetSessionToolCalls returns archived tool calls for a session in chronological order.
func (s *ArchiveStore) GetSessionToolCalls(sessionID string) ([]ArchivedToolCall, error) {
	rows, err := s.db.Query(`
		SELECT id, conversation_id, session_id, tool_name, arguments,
		       result, error, started_at, completed_at, duration_ms,
		       COALESCE(archived_at, '') as archived_at,
		       iteration_index
		FROM tool_calls
		WHERE session_id = ?
		ORDER BY started_at ASC
	`, sessionID)
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

		if tc.StartedAt, err = database.ParseTimestamp(startStr); err != nil {
			return nil, fmt.Errorf("parse tool_call started_at: %w", err)
		}
		if archivedStr != "" {
			if tc.ArchivedAt, err = database.ParseTimestamp(archivedStr); err != nil {
				return nil, fmt.Errorf("parse tool_call archived_at: %w", err)
			}
		}
		if completedStr.Valid {
			ts, tsErr := database.ParseTimestamp(completedStr.String)
			if tsErr != nil {
				return nil, fmt.Errorf("parse tool_call completed_at: %w", tsErr)
			}
			tc.CompletedAt = &ts
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

		if iter.StartedAt, err = database.ParseTimestamp(startStr); err != nil {
			return nil, fmt.Errorf("parse iteration started_at: %w", err)
		}
		if breakReason.Valid {
			iter.BreakReason = breakReason.String
		}
		if toolCallIDsJSON.Valid {
			if unmarshalErr := json.Unmarshal([]byte(toolCallIDsJSON.String), &iter.ToolCallIDs); unmarshalErr != nil && s.logger != nil {
				s.logger.Debug("failed to unmarshal tool_call_ids", "session_id", iter.SessionID, "error", unmarshalErr)
			}
		}
		if toolsOfferedJSON.Valid {
			if unmarshalErr := json.Unmarshal([]byte(toolsOfferedJSON.String), &iter.ToolsOffered); unmarshalErr != nil && s.logger != nil {
				s.logger.Debug("failed to unmarshal tools_offered", "session_id", iter.SessionID, "error", unmarshalErr)
			}
		}

		iters = append(iters, iter)
	}
	return iters, rows.Err()
}

// LinkToolCallsToIteration sets the iteration_index on tool calls that
// belong to a specific iteration within a session.
func (s *ArchiveStore) LinkToolCallsToIteration(sessionID string, iterationIndex int, toolCallIDs []string) error {
	if len(toolCallIDs) == 0 {
		return nil
	}

	db := s.db
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		UPDATE tool_calls SET iteration_index = ?
		WHERE id = ? AND session_id = ?
	`)
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
// tool_calls rows. Call this after tool calls have been assigned to the session
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
// Callers with a request context should use [ArchiveStore.SearchContext].
func (s *ArchiveStore) Search(opts SearchOptions) ([]SearchResult, error) {
	return s.SearchContext(context.Background(), opts)
}

// SearchContext performs a cancellable full-text search with gap-aware context
// expansion. Context windows exclude the matching timestamp and stop at silence,
// duration, and message-count boundaries. Query, decoding, and context-expansion
// failures return an error without partial results.
func (s *ArchiveStore) SearchContext(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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

	// Run the search. FTS5 path uses phrase-first + OR-of-terms
	// backfill so multi-word queries get phrase-anchored precision
	// at the top with recall headroom when the phrase is sparse.
	// LIKE path is the FTS5-unavailable fallback.
	var matches []matchWithHighlight
	var err error
	if s.ftsEnabled {
		matches, err = s.searchFTS(ctx, opts)
	} else {
		matches, err = s.searchLIKE(ctx, opts)
	}
	if err != nil {
		return nil, err
	}

	// Now expand context for each match (safe to query again)
	var results []SearchResult
	for _, mh := range matches {
		var before, after []Message
		if !opts.NoContext {
			before, err = s.expandContext(ctx, mh.msg.ConversationID, mh.msg.Timestamp, true, opts)
			if err != nil {
				return nil, fmt.Errorf("expand context before message %s: %w", mh.msg.ID, err)
			}
			after, err = s.expandContext(ctx, mh.msg.ConversationID, mh.msg.Timestamp, false, opts)
			if err != nil {
				return nil, fmt.Errorf("expand context after message %s: %w", mh.msg.ID, err)
			}
		}

		results = append(results, SearchResult{
			Match:         mh.msg,
			SessionID:     mh.msg.SessionID,
			ContextBefore: before,
			ContextAfter:  after,
			Highlight:     mh.highlight,
			Score:         mh.score,
			MatchType:     mh.matchType,
		})
	}

	return results, nil
}

// matchWithHighlight is the per-row shape both searchFTS and
// searchLIKE produce. It carries the full archived message plus
// the FTS5 snippet highlight (or empty string for the LIKE
// fallback), and lets [Search] do context expansion separately
// from the candidate-collection phase.
type matchWithHighlight struct {
	msg       Message
	highlight string
	score     float64 // presented relevance (higher = better; negated BM25)
	matchType string  // "phrase" | "terms" | "" (LIKE fallback)
}

// searchFTS runs the phrase-first + OR-of-terms backfill against
// the FTS5 index. The phrase pass scores rows that contain the
// literal query as a phrase (precision); when phrase hits are
// fewer than opts.Limit, the backfill pass adds OR-of-terms hits
// (recall headroom) without disturbing the phrase-anchored
// ordering. Both passes share the same anticipation and
// conversation-ID filters because they both go through runFTSQuery.
//
// Single-word queries skip the backfill entirely: the OR form
// `"word"` is identical to the phrase form, so a second query
// would only produce duplicates.
func (s *ArchiveStore) searchFTS(ctx context.Context, opts SearchOptions) ([]matchWithHighlight, error) {
	phrase := phraseFTS5Query(opts.Query)
	if phrase == "" {
		return nil, fmt.Errorf("query is required")
	}

	phraseHits, err := s.runFTSQuery(ctx, phrase, opts, opts.Limit)
	if err != nil {
		return nil, err
	}
	tagMatchType(phraseHits, "phrase")
	if len(phraseHits) >= opts.Limit {
		return phraseHits, nil
	}

	// Backfill with the broader OR-of-terms shape. Over-fetch a bit
	// so that dedup against the phrase hits still leaves headroom
	// to reach opts.Limit.
	orExpr := orFTS5Query(opts.Query)
	if orExpr == "" || orExpr == phrase {
		// Single-word query — backfill would be identical, skip it.
		return phraseHits, nil
	}
	backfill, err := s.runFTSQuery(ctx, orExpr, opts, opts.Limit*2)
	if err != nil {
		return nil, err
	}
	tagMatchType(backfill, "terms")
	return mergeFTSMatches(phraseHits, backfill, opts.Limit), nil
}

// tagMatchType stamps the provenance pass onto each hit so the envelope
// can tell the model which matches are exact-phrase (precision) versus
// OR-of-terms backfill (recall headroom).
func tagMatchType(matches []matchWithHighlight, kind string) {
	for i := range matches {
		matches[i].matchType = kind
	}
}

// runFTSQuery executes one FTS5 expression against messages_fts
// and returns the matching rows in BM25 rank order. Filters
// (conversation_id, anticipation exclusion) live in the SQL WHERE
// clause so they participate in BM25 scoring rather than being
// applied post-hoc.
func (s *ArchiveStore) runFTSQuery(ctx context.Context, ftsExpr string, opts SearchOptions, limit int) ([]matchWithHighlight, error) {

	query := fmt.Sprintf(`
		SELECT %s,
		       snippet(messages_fts, 0, '**', '**', '...', 64) as highlight,
		       bm25(messages_fts) as score
		FROM messages_fts
		JOIN messages am ON messages_fts.rowid = am.rowid
	`, ftsMatchColumns)

	conditions, args := s.ftsConditions(ftsExpr, opts)
	query += " WHERE " + strings.Join(conditions, " AND ")
	query += " ORDER BY rank LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	return scanFTSMatches(rows)
}

// ftsMatchColumns is the message-column projection shared by the FTS
// SELECT here and the COUNT/scan paths, kept in one place so the column
// order can't drift from [scanFTSMatches].
const ftsMatchColumns = "am.id, am.conversation_id, COALESCE(am.session_id, '') as session_id, am.role, am.content, am.timestamp, am.token_count, am.tool_calls, am.tool_call_id, COALESCE(am.archived_at, '') as archived_at, COALESCE(am.archive_reason, '') as archive_reason, COALESCE(am.origin, '') as origin"

// ftsConditions builds the shared WHERE clause and bound args for an
// FTS query: the MATCH expression plus optional conversation scope,
// anticipation exclusion, and time-range bounds. Shared by
// [runFTSQuery] and [countMatches] so the candidate set BM25 ranks over
// and the total it is drawn from never diverge.
//
// The anticipation exclusion drops wake-bridge synthetic content
// ("Anticipation matched: …"), which has its own retrieval paths and
// whose dense-keyword shape would otherwise dominate BM25.
//
// Time bounds are compared through SQLite's datetime() on both sides so
// imports and native writes share the same filter despite their historical
// timestamp layouts (RFC3339Nano versus SQLite's space-separated form).
// A raw string compare would silently mismatch at the window edges
// (" " 0x20 < "T" 0x54); datetime() normalizes both forms
// to a canonical second-granular value first. The candidate set is
// already constrained by the FTS MATCH, so the per-row datetime() call
// is cheap.
func (s *ArchiveStore) ftsConditions(ftsExpr string, opts SearchOptions) ([]string, []any) {
	conditions := []string{"messages_fts MATCH ?"}
	args := []any{ftsExpr}
	if opts.ConversationID != "" {
		conditions = append(conditions, "am.conversation_id = ?")
		args = append(args, opts.ConversationID)
	}
	if !opts.IncludeAnticipations {
		conditions = append(conditions, "am.content NOT LIKE 'Anticipation matched:%'")
	}
	if !opts.From.IsZero() {
		conditions = append(conditions, "datetime(am.timestamp) >= datetime(?)")
		args = append(args, opts.From.UTC().Format(time.RFC3339Nano))
	}
	if !opts.To.IsZero() {
		conditions = append(conditions, "datetime(am.timestamp) <= datetime(?)")
		args = append(args, opts.To.UTC().Format(time.RFC3339Nano))
	}
	return conditions, args
}

// countMatches returns how many archived messages match ftsExpr under
// the same filters runFTSQuery applies, before the result limit. It
// powers the envelope's total_estimated overflow gauge.
func (s *ArchiveStore) countMatches(ctx context.Context, ftsExpr string, opts SearchOptions) (int, error) {
	conditions, args := s.ftsConditions(ftsExpr, opts)
	query := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM messages_fts
		JOIN messages am ON messages_fts.rowid = am.rowid
		WHERE %s
	`, strings.Join(conditions, " AND "))
	var n int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count matches: %w", err)
	}
	return n, nil
}

// CountMatches estimates the total raw-message hits for a query under
// the given options — the broadest (OR-of-terms) recall set, so it
// answers "how many messages match any of these terms" rather than the
// narrower phrase count. Returns 0 with no error when FTS5 is
// unavailable (the LIKE fallback path skips the estimate). Used by
// [MemorySearch.Search] to populate the envelope's total_estimated.
func (s *ArchiveStore) CountMatches(opts SearchOptions) (int, error) {
	return s.CountMatchesContext(context.Background(), opts)
}

// CountMatchesContext is [ArchiveStore.CountMatches] with caller cancellation.
func (s *ArchiveStore) CountMatchesContext(ctx context.Context, opts SearchOptions) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !s.ftsEnabled {
		return 0, nil
	}
	expr := orFTS5Query(opts.Query)
	if expr == "" {
		return 0, nil
	}
	return s.countMatches(ctx, expr, opts)
}

// searchLIKE runs the FTS5-unavailable fallback path. Less
// precise (substring match, no BM25 ranking) but functional. The
// anticipation filter applies here too — same rationale as the
// FTS path, just enforced with a NOT LIKE clause.
func (s *ArchiveStore) searchLIKE(ctx context.Context, opts SearchOptions) ([]matchWithHighlight, error) {
	cols := s.msgSelectCols()

	query := fmt.Sprintf(`
		SELECT %s,
		       '' as highlight,
		       0.0 as score
		FROM messages
	`, cols)
	args := []any{"%" + opts.Query + "%"}
	conditions := []string{"content LIKE ?"}

	if opts.ConversationID != "" {
		conditions = append(conditions, "conversation_id = ?")
		args = append(args, opts.ConversationID)
	}
	if !opts.IncludeAnticipations {
		conditions = append(conditions, "content NOT LIKE 'Anticipation matched:%'")
	}
	// Time-range facet applies on the fallback path too — the feature
	// must not silently no-op when FTS5 is unavailable. datetime() on
	// both sides keeps the compare storage-format agnostic (see
	// ftsConditions).
	if !opts.From.IsZero() {
		conditions = append(conditions, "datetime(timestamp) >= datetime(?)")
		args = append(args, opts.From.UTC().Format(time.RFC3339Nano))
	}
	if !opts.To.IsZero() {
		conditions = append(conditions, "datetime(timestamp) <= datetime(?)")
		args = append(args, opts.To.UTC().Format(time.RFC3339Nano))
	}

	query += " WHERE " + strings.Join(conditions, " AND ")
	query += " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, opts.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	return scanFTSMatches(rows)
}

// scanFTSMatches reads the shared match-with-highlight projection
// out of either the FTS5 or LIKE query path. Both queries select
// the same column list so a single scanner serves both.
func scanFTSMatches(rows *sql.Rows) ([]matchWithHighlight, error) {
	var matches []matchWithHighlight
	for rows.Next() {
		var m Message
		var highlight string
		var bm25 float64
		var tsStr, archivedStr string
		var toolCalls, toolCallID sql.NullString

		err := rows.Scan(
			&m.ID, &m.ConversationID, &m.SessionID, &m.Role, &m.Content,
			&tsStr, &m.TokenCount, &toolCalls, &toolCallID,
			&archivedStr, &m.ArchiveReason, &m.Origin, &highlight, &bm25,
		)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}

		if m.Timestamp, err = database.ParseTimestamp(tsStr); err != nil {
			return nil, fmt.Errorf("parse message timestamp: %w", err)
		}
		if archivedStr != "" {
			if m.ArchivedAt, err = database.ParseTimestamp(archivedStr); err != nil {
				return nil, fmt.Errorf("parse message archived_at: %w", err)
			}
		}
		if toolCalls.Valid {
			m.ToolCalls = toolCalls.String
		}
		if toolCallID.Valid {
			m.ToolCallID = toolCallID.String
		}

		// BM25 returns more-negative for better matches; negate so the
		// model-facing score reads higher = more relevant. The LIKE
		// fallback selects 0.0, which negates to a harmless 0.
		matches = append(matches, matchWithHighlight{msg: m, highlight: highlight, score: -bm25})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate results: %w", err)
	}
	return matches, nil
}

// mergeFTSMatches appends backfill rows after the phrase-anchored
// rows, dropping duplicates by message ID and capping at limit.
// Phrase ordering is preserved at the head; backfill ordering
// (BM25 over OR-of-terms) fills the tail.
func mergeFTSMatches(phrase, backfill []matchWithHighlight, limit int) []matchWithHighlight {
	if len(phrase) >= limit {
		return phrase[:limit]
	}
	seen := make(map[string]struct{}, len(phrase)+len(backfill))
	for _, m := range phrase {
		seen[m.msg.ID] = struct{}{}
	}
	out := make([]matchWithHighlight, 0, limit)
	out = append(out, phrase...)
	for _, m := range backfill {
		if _, dup := seen[m.msg.ID]; dup {
			continue
		}
		out = append(out, m)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// expandContext walks messages outward from a timestamp, stopping at silence
// gaps. It shares exact timestamp normalization and error handling with range
// retrieval so imported and native rows produce the same context window.
func (s *ArchiveStore) expandContext(
	ctx context.Context,
	conversationID string,
	from time.Time,
	backward bool,
	opts SearchOptions,
) ([]Message, error) {
	boundary := from.Add(opts.MaxDuration)
	q := messageRangeQuery{
		from: &from, to: &boundary,
		fromExclusive: true, toExclusive: true,
		conversationID: conversationID,
		limit:          opts.MaxMessages,
		newest:         backward,
	}
	if backward {
		boundary = from.Add(-opts.MaxDuration)
		q.from, q.to = &boundary, &from
	}
	candidates, err := s.queryMessagesRange(ctx, q)
	if err != nil {
		return nil, err
	}

	var messages []Message
	prevTime := from
	for _, m := range candidates {
		gap := m.Timestamp.Sub(prevTime)
		if backward {
			gap = prevTime.Sub(m.Timestamp)
		}
		if gap > opts.SilenceThreshold {
			break
		}
		messages = append(messages, m)
		prevTime = m.Timestamp
	}
	if backward {
		for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
			messages[i], messages[j] = messages[j], messages[i]
		}
	}
	return messages, nil
}

// StartSession creates a new session record with the current time.
func (s *ArchiveStore) StartSession(conversationID string) (*Session, error) {
	return s.StartSessionWithOptions(conversationID)
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
// parent linkage and metadata snapshots. Use [WithParentSession],
// [WithParentToolCall], and [WithChannelBinding] to stamp archive-only
// session context at creation time.
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
	metaJSON, err := sessionMetadataJSON(sess.Metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}

	if _, err = s.db.Exec(`
		INSERT INTO sessions (id, conversation_id, started_at, message_count, metadata, parent_session_id, parent_tool_call_id)
		VALUES (?, ?, ?, 0, ?, ?, ?)
	`, sess.ID, conversationID, sess.StartedAt.Format(time.RFC3339Nano),
		nullString(string(metaJSON)), nullString(sess.ParentSessionID), nullString(sess.ParentToolCallID)); err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}

	return sess, nil
}

// EndSession marks a session as ended at the current time.
func (s *ArchiveStore) EndSession(sessionID string, reason string) error {
	return s.EndSessionAt(sessionID, reason, time.Now().UTC())
}

// EndSessionAt marks a session as ended at a specific time. After the
// commit succeeds, any [SetSessionCloseCallback] registered on the
// store fires synchronously with (sessionID, reason). The session
// state change is authoritative; the callback is best-effort
// notification and any panic / slow execution there does NOT roll back
// the DB write.
func (s *ArchiveStore) EndSessionAt(sessionID string, reason string, endedAt time.Time) error {
	if err := s.endSessionAt(sessionID, reason, endedAt); err != nil {
		return err
	}
	s.notifySessionClosed(sessionID, reason)
	return nil
}

// endSessionAt performs the durable write without notification so adapters
// can publish their cache and release lifecycle locks before callbacks run.
func (s *ArchiveStore) endSessionAt(sessionID string, reason string, endedAt time.Time) error {
	_, err := s.db.Exec(`
		UPDATE sessions SET ended_at = ?, end_reason = ? WHERE id = ?
	`, endedAt.Format(time.RFC3339Nano), reason, sessionID)
	return err
}

func (s *ArchiveStore) notifySessionClosed(sessionID, reason string) {
	if cb := s.sessionCloseCallback; cb != nil {
		// Defer-recover guard: a bad callback should never poison the
		// store's caller. The session is already closed — we just
		// failed to notify whoever cared.
		func() {
			defer func() {
				if r := recover(); r != nil && s.logger != nil {
					s.logger.Error("session close callback panicked",
						"session", ShortID(sessionID),
						"panic", r,
					)
				}
			}()
			cb(sessionID, reason)
		}()
	}
}

// SetSessionCloseCallback registers a function to be called after every
// successful EndSession / EndSessionAt commit. Passing nil clears the
// callback. The callback receives the just-closed session's ID and end
// reason and runs synchronously in the caller's goroutine — keep it
// fast and non-blocking (channel send, then return).
//
// Used by the app wiring to enqueue archivist work on session close (see
// issues #989, #1024). Single callback per store; calling this more than once
// replaces the previous callback rather than chaining.
func (s *ArchiveStore) SetSessionCloseCallback(cb func(sessionID, reason string)) {
	s.sessionCloseCallback = cb
}

// ClaimActiveMessages stamps session_id on active messages for a conversation
// so they become retrievable by GetSessionTranscript. This is needed when the
// summarizer's idle backstop closes a session — active messages in the unified
// table have session_id=NULL until archival, so without this step the
// transcript query returns nothing and the session is marked empty.
func (s *ArchiveStore) ClaimActiveMessages(conversationID, sessionID string) (int64, error) {
	db := s.db
	result, err := db.Exec(
		`UPDATE messages SET session_id = ? WHERE conversation_id = ? AND session_id IS NULL AND status = 'active'`,
		sessionID, conversationID,
	)
	if err != nil {
		return 0, fmt.Errorf("claim active messages: %w", err)
	}

	// Also claim any active tool calls so GetSessionToolCalls can find
	// them after the session is closed and summarized.
	_, err = db.Exec(
		`UPDATE tool_calls SET session_id = ? WHERE conversation_id = ? AND session_id IS NULL AND status = 'active'`,
		sessionID, conversationID,
	)
	if err != nil {
		return 0, fmt.Errorf("claim active tool calls: %w", err)
	}

	return result.RowsAffected()
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
	existingMeta, err := s.sessionMetadata(sessionID)
	if err != nil {
		return err
	}
	if existingMeta != nil && existingMeta.ChannelBinding != nil {
		if meta == nil {
			meta = &SessionMetadata{}
		}
		if meta.ChannelBinding == nil {
			meta.ChannelBinding = existingMeta.ChannelBinding.Clone()
		}
	}

	metaJSON, err := sessionMetadataJSON(meta)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
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

	_, err = s.db.Exec(`
		UPDATE sessions SET title = ?, tags = ?, metadata = ?, summary = ?
		WHERE id = ?
	`, nullString(title), nullString(string(tagsJSON)), nullString(string(metaJSON)), nullString(summary), sessionID)
	return err
}

func sessionMetadataJSON(meta *SessionMetadata) ([]byte, error) {
	if meta == nil {
		return nil, nil
	}
	return json.Marshal(meta)
}

func (s *ArchiveStore) sessionMetadata(sessionID string) (*SessionMetadata, error) {
	row := s.db.QueryRow(`SELECT metadata FROM sessions WHERE id = ?`, sessionID)
	var metaJSON sql.NullString
	if err := row.Scan(&metaJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("session not found: %s", sessionID)
		}
		return nil, fmt.Errorf("load session metadata: %w", err)
	}
	if !metaJSON.Valid || strings.TrimSpace(metaJSON.String) == "" {
		return nil, nil
	}
	var meta SessionMetadata
	if err := json.Unmarshal([]byte(metaJSON.String), &meta); err != nil {
		return nil, fmt.Errorf("parse session metadata: %w", err)
	}
	return &meta, nil
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

// ActiveSessionCount returns the number of unclosed (active) sessions.
// This is a lightweight query for telemetry dashboards — use
// [ArchiveStore.ActiveSessionsWithLastActivity] when per-session
// details are needed. It honors ctx because it runs on the telemetry
// collector's bounded refresh path.
func (s *ArchiveStore) ActiveSessionCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE ended_at IS NULL`).Scan(&count)
	return count, err
}

// ActiveSessionsWithLastActivity returns all unclosed sessions and the
// timestamp of the most recent message in each. Sessions with no messages
// use the session's started_at as the last activity time. This powers the
// summarizer's idle session detection — it survives crashes because it reads
// from the database rather than relying on in-memory state.
func (s *ArchiveStore) ActiveSessionsWithLastActivity() ([]IdleSessionInfo, error) {
	rows, err := s.db.Query(`
		SELECT id, conversation_id, started_at
		FROM sessions
		WHERE ended_at IS NULL
	`)
	if err != nil {
		return nil, fmt.Errorf("query active sessions: %w", err)
	}
	defer rows.Close()

	db := s.db
	var results []IdleSessionInfo
	for rows.Next() {
		var info IdleSessionInfo
		var startedAtStr string
		if err := rows.Scan(&info.SessionID, &info.ConversationID, &startedAtStr); err != nil {
			return nil, fmt.Errorf("scan active session: %w", err)
		}

		startedAt, err := database.ParseTimestamp(startedAtStr)
		if err != nil {
			// Skip sessions with unparseable timestamps rather than
			// risk closing them due to a zero-time default.
			continue
		}
		info.LastActivity = startedAt // default: session start time

		// Unclaimed active messages already belong to this conversation,
		// even before a lifecycle transition assigns their session ID.
		var maxTS sql.NullString
		err = db.QueryRow(`SELECT MAX(timestamp) FROM messages
			WHERE session_id = ?
			   OR (session_id IS NULL AND conversation_id = ? AND status = 'active')`,
			info.SessionID, info.ConversationID,
		).Scan(&maxTS)

		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("query last activity for session %s: %w", ShortID(info.SessionID), err)
		}

		if maxTS.Valid {
			if parsed, err := database.ParseTimestamp(maxTS.String); err == nil {
				info.LastActivity = parsed
			}
		}

		results = append(results, info)
	}
	return results, rows.Err()
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

	if err := s.populateMessageCounts(sessions); err != nil {
		return nil, fmt.Errorf("populate message counts: %w", err)
	}
	return sessions, nil
}

// ListClosedSessionsPage returns one stable, ID-keyset page of closed sessions
// that ended no later than cutoff. Results are ordered by session ID, and
// hasMore says whether another page remains after the last returned session.
// The cutoff freezes a one-time traversal while the normal session-close path
// continues to handle sessions that finish after it began.
//
// Session IDs are the cursor rather than ended_at because historical rows mix
// timestamp encodings. SQLite datetime() is used only for the inclusive cutoff;
// pagination itself stays exact and index-backed through the primary key.
func (s *ArchiveStore) ListClosedSessionsPage(cutoff time.Time, afterID string, limit int) ([]*Session, bool, error) {
	if cutoff.IsZero() {
		return nil, false, fmt.Errorf("closed session page cutoff is required")
	}
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.Query(`
		SELECT id, conversation_id, started_at, ended_at, end_reason,
		       0 AS message_count,
		       summary, title, tags, metadata, parent_session_id, parent_tool_call_id
		FROM sessions
		WHERE ended_at IS NOT NULL
		  AND datetime(ended_at) <= datetime(?)
		  AND id > ?
		ORDER BY id ASC
		LIMIT ?
	`, cutoff.UTC().Format(time.RFC3339Nano), strings.TrimSpace(afterID), limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list closed session page: %w", err)
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		sess, err := s.scanSessionRow(rows)
		if err != nil {
			return nil, false, err
		}
		sessions = append(sessions, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	hasMore := len(sessions) > limit
	if hasMore {
		sessions = sessions[:limit]
	}
	if err := s.populateMessageCounts(sessions); err != nil {
		return nil, false, fmt.Errorf("populate message counts: %w", err)
	}
	return sessions, hasMore, nil
}

// ListClosedSessionsEndedBefore returns closed sessions on the given
// conversation whose ended_at is strictly before cutoff, most recently
// ended first, with message counts populated. Paginated by limit and
// offset so callers that filter rows out (e.g. dropping empty
// sessions) can scan deeper instead of working from a fixed window.
// Time bounds and ordering go through SQLite's datetime() because
// stored session timestamps mix RFC3339 local-offset and driver-native
// forms; raw text comparison would misorder them (#761).
func (s *ArchiveStore) ListClosedSessionsEndedBefore(conversationID string, cutoff time.Time, limit, offset int) ([]*Session, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := s.db.Query(`
		SELECT id, conversation_id, started_at, ended_at, end_reason,
		       0 AS message_count,
		       summary, title, tags, metadata, parent_session_id, parent_tool_call_id
		FROM sessions
		WHERE conversation_id = ? AND ended_at IS NOT NULL
		  AND datetime(ended_at) < datetime(?)
		ORDER BY datetime(ended_at) DESC, datetime(started_at) DESC
		LIMIT ? OFFSET ?
	`, conversationID, cutoff.UTC().Format(time.RFC3339Nano), limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list closed sessions: %w", err)
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

	if err := s.populateMessageCounts(sessions); err != nil {
		return nil, fmt.Errorf("populate message counts: %w", err)
	}
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

	if err := s.populateMessageCounts(sessions); err != nil {
		return nil, fmt.Errorf("populate message counts: %w", err)
	}
	return sessions, nil
}

// UnsummarizedSessions returns ended sessions that have no metadata
// yet, ordered oldest-first for catch-up processing. Includes
// zero-message sessions (typically crash_recovery placeholders) —
// the summarizer worker's empty-transcript branch marks them
// summarized via markEmpty so they don't re-enter the queue, but
// they need to *enter* the queue first.
//
// Earlier revisions over-fetched and filtered out zero-message
// candidates here, intending to avoid wasted summarizer work. In
// practice that put 12,812 crash_recovery rows at the head of the
// queue (oldest ended_at), filled the over-fetch budget with them,
// and dropped them all in the filter — the worker returned zero
// candidates indefinitely and the real backlog never advanced. See
// issue #977 Finding 3b.
func (s *ArchiveStore) UnsummarizedSessions(limit int) ([]*Session, error) {
	if limit <= 0 {
		limit = 50
	}

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
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("unsummarized sessions: %w", err)
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

	// Populate MessageCount so the worker (and any other caller)
	// sees accurate counts on returned sessions, even though the
	// query no longer gates on them.
	if err := s.populateMessageCounts(sessions); err != nil {
		return nil, fmt.Errorf("populate message counts: %w", err)
	}
	return sessions, nil
}

// GetSessionTranscript returns all archived messages for a session in chronological order.
func (s *ArchiveStore) GetSessionTranscript(sessionID string) ([]Message, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM messages
		WHERE session_id = ?
		ORDER BY timestamp ASC
	`, s.msgSelectCols())

	rows, err := s.db.Query(query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get transcript: %w", err)
	}
	defer rows.Close()

	return s.scanMessages(rows)
}

// MaxArchiveRangeMessages is the hard row limit for archive time-range queries.
// HTTP queries default to 500 rows; model-context queries default to 200.
const MaxArchiveRangeMessages = 1000

// GetMessagesByTimeRange returns the oldest messages in the inclusive time
// range, ordered by instant then message ID. It includes every message status
// in the unified store, optionally restricted to conversationID. Non-positive
// limit uses 500; limits above MaxArchiveRangeMessages are clamped. Timestamp
// layouts and time zones do not affect filtering or ordering. Cancellation and
// invalid stored timestamps are returned as errors, never partial success.
func (s *ArchiveStore) GetMessagesByTimeRange(ctx context.Context, from, to time.Time, conversationID string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 500
	}
	limit = min(limit, MaxArchiveRangeMessages)
	return s.queryMessagesRange(ctx, messageRangeQuery{from: &from, to: &to, conversationID: conversationID, limit: limit})
}

// RangeOptions configures [ArchiveStore.GetMessagesInRange]. All fields
// are optional — the zero value of RangeOptions returns the most recent
// 200 messages across all conversations, ordered chronologically.
type RangeOptions struct {
	// ConversationID restricts the result to a single conversation when
	// non-empty. Empty matches all conversations.
	ConversationID string `json:"conversation_id,omitempty"`

	// ExcludeSessionID drops messages from the named session when
	// non-empty. Useful for system-prompt context providers that want
	// archived/older messages but not the active session's currently
	// in-memory rows (which the model already sees in its working
	// message list).
	ExcludeSessionID string `json:"exclude_session_id,omitempty"`

	// From is the earliest timestamp to include (inclusive). Nil means
	// unbounded — combined with MinMessages, this is how the "give me at
	// least N most-recent messages regardless of age" query is expressed.
	// A non-nil zero time is the explicit year-one boundary.
	From *time.Time `json:"from,omitempty"`

	// To is the latest timestamp to include (inclusive). Nil defaults to
	// time.Now() at query time; a non-nil zero time remains an exact bound.
	To *time.Time `json:"to,omitempty"`

	// MinMessages widens the window to older matching history when fewer
	// than this many messages fall inside [From, To], subject to the
	// MaxMessages cap and available history. Zero disables the floor.
	// Useful for "last X minutes OR Y messages, whichever is more".
	MinMessages int `json:"min_messages,omitempty"`

	// MaxMessages caps the result. Non-positive uses the default of 200.
	// Values above MaxArchiveRangeMessages are clamped; the floor cannot
	// exceed this cap.
	// When the cap clips the result, the second return value of
	// GetMessagesInRange is true.
	MaxMessages int `json:"max_messages,omitempty"`
}

// GetMessagesInRange returns archived messages bounded by time, with an
// optional MinMessages floor, limited by the effective MaxMessages cap. It
// selects the newest matching messages and returns them ordered by instant then
// message ID (oldest first), including all statuses in the unified store. Bounds
// are inclusive and exact to the nanosecond across supported layouts and zones.
// The boolean return is true when the cap clipped the result. Cancellation and
// invalid stored timestamps are errors, never partial success.
func (s *ArchiveStore) GetMessagesInRange(ctx context.Context, opts RangeOptions) ([]Message, bool, error) {
	maxN := opts.MaxMessages
	if maxN <= 0 {
		maxN = 200
	}
	maxN = min(maxN, MaxArchiveRangeMessages)
	if opts.To == nil {
		now := time.Now()
		opts.To = &now
	}

	query := messageRangeQuery{
		from:             opts.From,
		to:               opts.To,
		conversationID:   opts.ConversationID,
		excludeSessionID: opts.ExcludeSessionID,
		limit:            maxN + 1,
		newest:           true,
	}

	// Step 1: most recent messages within [from, to], DESC. Ask for one
	// more than the cap so we can detect truncation.
	msgs, err := s.queryMessagesRange(ctx, query)
	if err != nil {
		return nil, false, err
	}
	truncated := len(msgs) > maxN
	if truncated {
		msgs = msgs[:maxN]
	}

	// Step 2: floor — if the in-window query under-delivered, drop the
	// From bound entirely and re-query for the most recent up-to-MaxN
	// messages. MinMessages is a floor ("at least this many"), not a
	// cap — when it triggers we still return up to MaxMessages so the
	// model gets useful context, not exactly MinMessages.
	if opts.From != nil && opts.MinMessages > 0 && len(msgs) < min(opts.MinMessages, maxN) {
		query.from = nil
		floor, err := s.queryMessagesRange(ctx, query)
		if err != nil {
			return nil, false, err
		}
		truncated = len(floor) > maxN
		if truncated {
			floor = floor[:maxN]
		}
		msgs = floor
	}

	// Reverse DESC → chronological for output.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, truncated, nil
}

// RecentContactTimes returns the timestamps of the most recent
// contact-classified messages on a conversation, newest first, capped at
// limit. Contact means communication with the channel counterparty:
// rows stamped [OriginChannel] in either direction, plus — the one
// documented fallback for rows written before provenance stamping
// existed — unstamped user-role rows that are not wake-bridge synthetic
// content (the "Anticipation matched:" prefix, the same exclusion the
// search paths apply). Wake prompts (OriginWake) and system-authored
// rows never move the result: the previous contact is the anchor the
// conversation-timing narrative measures gaps against, and a wake that
// shifted it would erase the very silence it is reporting.
//
// The current turn's just-stored inbound message is included when it is
// already in the table — callers on a contact turn take the second entry
// as the previous contact, and wake-turn callers take the first.
func (s *ArchiveStore) RecentContactTimes(conversationID string, limit int) ([]time.Time, error) {
	if limit <= 0 {
		limit = 2
	}
	query := `
		SELECT timestamp FROM messages
		WHERE conversation_id = ?
		  AND (
			origin = ?
			OR (COALESCE(origin, '') = '' AND role = 'user' AND content NOT LIKE 'Anticipation matched:%')
		  )
		ORDER BY timestamp DESC, id DESC
		LIMIT ?
	`

	rows, err := s.db.Query(query, conversationID, OriginChannel, limit)
	if err != nil {
		return nil, fmt.Errorf("recent contact times: %w", err)
	}
	defer rows.Close()

	var out []time.Time
	for rows.Next() {
		var tsStr string
		if err := rows.Scan(&tsStr); err != nil {
			return nil, fmt.Errorf("scan contact time: %w", err)
		}
		ts, err := database.ParseTimestamp(tsStr)
		if err != nil {
			return nil, fmt.Errorf("parse contact time: %w", err)
		}
		out = append(out, ts)
	}
	return out, rows.Err()
}

// Pointer bounds distinguish an omitted bound from the valid year-one zero
// instant accepted by the HTTP and tool range contracts.
type messageRangeQuery struct {
	from, to         *time.Time
	fromExclusive    bool
	toExclusive      bool
	conversationID   string
	excludeSessionID string
	limit            int
	newest           bool
}

// queryMessagesRange owns range filtering and deterministic selection for
// archive range readers and search context windows. Normalizing on read preserves
// historical timestamp bytes and exact sub-millisecond boundaries. This requires
// scanning candidate rows; QueryContext makes that work cancellable. Do not replace it with SQLite
// strftime: its millisecond rounding changes inclusive boundaries.
func (s *ArchiveStore) queryMessagesRange(ctx context.Context, q messageRangeQuery) ([]Message, error) {
	if q.from != nil && q.to != nil && q.from.After(*q.to) {
		return nil, fmt.Errorf("from must not be after to")
	}
	clauses := []string{"1 = 1"}
	var args []any
	if q.conversationID != "" {
		clauses = append(clauses, "conversation_id = @conversation")
		args = append(args, sql.Named("conversation", q.conversationID))
	}
	if q.excludeSessionID != "" {
		// Keep unclaimed unified rows: SQL NULL != id does not match.
		clauses = append(clauses, "(session_id IS NULL OR session_id != @excluded_session)")
		args = append(args, sql.Named("excluded_session", q.excludeSessionID))
	}
	key := "thane_timestamp_key(timestamp)"
	if len(clauses) > 1 {
		// SQLite may evaluate timestamp predicates before other filters.
		// CASE is lazy, so malformed timestamps outside the requested
		// conversation/session scope cannot fail this query.
		key = "CASE WHEN " + strings.Join(clauses, " AND ") + " THEN " + key + " END"
	}
	if q.from != nil {
		op := " >= "
		if q.fromExclusive {
			op = " > "
		}
		clauses = append(clauses, key+op+"@from_time")
		args = append(args, sql.Named("from_time", database.TimestampKey(*q.from)))
	}
	if q.to != nil {
		op := " <= "
		if q.toExclusive {
			op = " < "
		}
		clauses = append(clauses, key+op+"@to_time")
		args = append(args, sql.Named("to_time", database.TimestampKey(*q.to)))
	}
	order := "ASC"
	if q.newest {
		order = "DESC"
	}
	args = append(args, sql.Named("limit", q.limit))
	query := fmt.Sprintf(`
		SELECT %s FROM messages WHERE %s
		ORDER BY %s %s, id %s LIMIT @limit
	`, s.msgSelectCols(), strings.Join(clauses, " AND "), key, order, order)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query messages by time range: %w", err)
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

	var msgCount, sessionCount, toolCallCount int
	var oldestStr, newestStr sql.NullString

	_ = s.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&msgCount)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessionCount)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&toolCallCount)
	_ = s.db.QueryRow(`SELECT MIN(timestamp) FROM messages`).Scan(&oldestStr)
	_ = s.db.QueryRow(`SELECT MAX(timestamp) FROM messages`).Scan(&newestStr)

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
	rows, err := s.db.Query(`SELECT role, COUNT(*) FROM messages GROUP BY role`)
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

	byStatus := make(map[string]int)
	rows2, err := s.db.Query(`SELECT COALESCE(status, 'unknown'), COUNT(*) FROM messages GROUP BY status`)
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

	if err := populateSession(&sess, startStr, endStr, endReason, summary, title, tagsJSON, metaJSON, parentSessionID, parentToolCallID, s.logger); err != nil {
		return nil, err
	}
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

	if err := populateSession(&sess, startStr, endStr, endReason, summary, title, tagsJSON, metaJSON, parentSessionID, parentToolCallID, s.logger); err != nil {
		return nil, err
	}
	return &sess, nil
}

// populateSession fills parsed fields from nullable database columns.
func populateSession(sess *Session, startStr string, endStr, endReason, summary, title, tagsJSON, metaJSON, parentSessionID, parentToolCallID sql.NullString, logger *slog.Logger) error {
	var err error
	if sess.StartedAt, err = database.ParseTimestamp(startStr); err != nil {
		return fmt.Errorf("parse session started_at: %w", err)
	}
	if endStr.Valid {
		ts, tsErr := database.ParseTimestamp(endStr.String)
		if tsErr != nil {
			return fmt.Errorf("parse session ended_at: %w", tsErr)
		}
		sess.EndedAt = &ts
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
	return nil
}

func (s *ArchiveStore) scanMessages(rows *sql.Rows) ([]Message, error) {
	var messages []Message
	for rows.Next() {
		var m Message
		var tsStr, archivedStr string
		var toolCalls, toolCallID sql.NullString

		err := rows.Scan(
			&m.ID, &m.ConversationID, &m.SessionID, &m.Role, &m.Content,
			&tsStr, &m.TokenCount, &toolCalls, &toolCallID,
			&archivedStr, &m.ArchiveReason, &m.Origin,
		)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}

		if m.Timestamp, err = database.ParseTimestamp(tsStr); err != nil {
			return nil, fmt.Errorf("parse message timestamp: %w", err)
		}
		if archivedStr != "" {
			if m.ArchivedAt, err = database.ParseTimestamp(archivedStr); err != nil {
				return nil, fmt.Errorf("parse message archived_at: %w", err)
			}
		}
		if toolCalls.Valid {
			m.ToolCalls = toolCalls.String
		}
		if toolCallID.Valid {
			m.ToolCallID = toolCallID.String
		}

		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read messages: %w", err)
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

// PurgeImported removes sessions, messages, tool calls, and import metadata
// imported from a given source type, allowing the import to be rerun. All
// deletions commit together in the shared database; FTS triggers synchronize
// message removal. The return value counts the imported sessions removed.
func (s *ArchiveStore) PurgeImported(sourceType string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin purge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(`SELECT archive_session_id FROM import_metadata WHERE source_type = ?`, sourceType)
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
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate import sessions: %w", err)
	}
	rows.Close()
	if len(sessionIDs) == 0 {
		return 0, nil
	}

	// Remove child references before their session parents, including
	// when the shared database enforces foreign keys.
	if _, err := tx.Exec(`DELETE FROM import_metadata WHERE source_type = ?`, sourceType); err != nil {
		return 0, fmt.Errorf("delete import metadata: %w", err)
	}
	for _, sid := range sessionIDs {
		if _, err := tx.Exec(`DELETE FROM tool_calls WHERE session_id = ?`, sid); err != nil {
			return 0, fmt.Errorf("delete tool calls for session %s: %w", ShortID(sid), err)
		}
		if _, err := tx.Exec(`DELETE FROM messages WHERE session_id = ?`, sid); err != nil {
			return 0, fmt.Errorf("delete messages for session %s: %w", ShortID(sid), err)
		}
		if _, err := tx.Exec(`DELETE FROM sessions WHERE id = ?`, sid); err != nil {
			return 0, fmt.Errorf("delete session %s: %w", ShortID(sid), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit purge: %w", err)
	}
	return len(sessionIDs), nil
}

// ShortID safely truncates an ID to 8 characters for display.
// Returns the full string if shorter than 8 characters.
func ShortID(id string) string {
	return promptfmt.ShortIDPrefix(id)
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// phraseFTS5Query wraps the whole query in a single double-quoted
// phrase so FTS5 matches only rows containing the literal sequence
// of terms. Embedded double-quotes are escaped per FTS5 string-
// literal rules. Returns "" for an empty input.
//
// Used as the precision-first leg of Search's phrase-first +
// OR-of-terms backfill: phrase hits rank by BM25 against rows that
// actually contain the phrase, instead of being drowned out by
// dense-keyword rows that happen to match individual tokens.
func phraseFTS5Query(query string) string {
	q := strings.TrimSpace(query)
	if q == "" {
		return ""
	}
	q = strings.ReplaceAll(q, `"`, `""`)
	return `"` + q + `"`
}

// orFTS5Query wraps each whitespace-separated term in double quotes
// and joins with OR. Broader recall than [phraseFTS5Query] — used
// as the backfill leg of Search's phrase-first + OR-of-terms
// strategy when the phrase pass returns fewer hits than the limit
// (or for single-word queries, where the two shapes are
// equivalent).
//
// E.g., "consciousness hard problem" becomes
// `"consciousness" OR "hard" OR "problem"`.
func orFTS5Query(query string) string {
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
