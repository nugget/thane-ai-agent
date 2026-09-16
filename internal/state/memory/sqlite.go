package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	_ "modernc.org/sqlite"
)

// SQLiteStore is a SQLite-backed memory store.
type SQLiteStore struct {
	db          *sql.DB
	maxMessages int
	logger      *slog.Logger

	// clipWarnAt rate-limits the "read window clipped" warning per
	// conversation. GetMessages returns at most maxMessages recent rows;
	// a conversation whose active set exceeds that is read every turn, so
	// an unthrottled warning would spam. Guarded by clipWarnMu.
	clipWarnMu sync.Mutex
	clipWarnAt map[string]time.Time
}

// NewSQLiteStore creates a new SQLite-backed store.
func NewSQLiteStore(dbPath string, maxMessages int) (*SQLiteStore, error) {
	return NewSQLiteStoreWithLogger(dbPath, maxMessages, nil)
}

// NewSQLiteStoreWithLogger creates a new SQLite-backed store and uses
// logger for non-fatal data-integrity warnings encountered while
// reading existing rows. Nil falls back to [slog.Default].
func NewSQLiteStoreWithLogger(dbPath string, maxMessages int, logger *slog.Logger) (*SQLiteStore, error) {
	if maxMessages <= 0 {
		maxMessages = 100
	}
	if logger == nil {
		logger = slog.Default()
	}

	db, err := database.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	store := &SQLiteStore{
		db:          db,
		maxMessages: maxMessages,
		logger:      logger,
		clipWarnAt:  make(map[string]time.Time),
	}

	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return store, nil
}

// migrate applies the working-memory schema declared in schema.go.
func (s *SQLiteStore) migrate() error {
	return database.Migrate(s.db, schema, s.logger)
}

// DB returns the underlying database connection for use by the unification
// migration and by ArchiveStore when reading from the unified messages table.
func (s *SQLiteStore) DB() *sql.DB {
	return s.db
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// GetOrCreateConversation ensures a conversation exists and returns it.
func (s *SQLiteStore) GetOrCreateConversation(id string) (*Conversation, error) {
	now := time.Now()

	// Try to insert, ignore if exists
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO conversations (id, created_at, updated_at)
		VALUES (?, ?, ?)
	`, id, now, now)
	if err != nil {
		return nil, fmt.Errorf("create conversation: %w", err)
	}

	row := s.db.QueryRow(`
		SELECT id, created_at, updated_at, metadata
		FROM conversations
		WHERE id = ?
	`, id)

	var conv Conversation
	var metadata sql.NullString
	if err := row.Scan(&conv.ID, &conv.CreatedAt, &conv.UpdatedAt, &metadata); err != nil {
		return nil, fmt.Errorf("load conversation: %w", err)
	}
	if metadata.Valid {
		meta, err := parseConversationMetadata(metadata.String)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("conversation metadata is invalid JSON; treating metadata as nil",
					"conversation_id", id,
					"error", err,
				)
			}
		} else {
			conv.Metadata = meta
		}
	}
	if conv.CreatedAt.IsZero() {
		conv.CreatedAt = now
	}
	if conv.UpdatedAt.IsZero() {
		conv.UpdatedAt = now
	}
	return &conv, nil
}

// AddMessage adds a message to a conversation. origin records how the
// message entered the conversation (see the Origin* constants); pass ""
// when the enqueue site cannot know.
func (s *SQLiteStore) AddMessage(conversationID, role, content, origin string) error {
	return s.addMessage(conversationID, role, content, origin, false)
}

// AddMidTurnMessage adds a message that arrived mid-turn and was merged into
// an in-flight turn (#1230), tagging the row so consumers can identify the
// injection from the structured record rather than substring-matching the
// channel-rendered arrival marker in the content. origin follows the same
// contract as [SQLiteStore.AddMessage].
func (s *SQLiteStore) AddMidTurnMessage(conversationID, role, content, origin string) error {
	return s.addMessage(conversationID, role, content, origin, true)
}

func (s *SQLiteStore) addMessage(conversationID, role, content, origin string, midTurn bool) error {
	now := time.Now()
	msgID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate message ID: %w", err)
	}

	// Ensure conversation exists
	_, err = s.GetOrCreateConversation(conversationID)
	if err != nil {
		return err
	}

	midTurnVal := 0
	if midTurn {
		midTurnVal = 1
	}

	// Insert message
	_, err = s.db.Exec(`
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count, mid_turn, origin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, msgID.String(), conversationID, role, content, now, llm.EstimateTokens(content), midTurnVal, origin)
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}

	// Update conversation timestamp
	_, err = s.db.Exec(`
		UPDATE conversations SET updated_at = ? WHERE id = ?
	`, now, conversationID)
	if err != nil {
		return fmt.Errorf("update conversation: %w", err)
	}

	return nil
}

// GetMessages retrieves the working-memory window for a conversation:
// the newest maxMessages active rows plus every active compaction
// summary, returned in chronological (ASC) order. Query, row-decoding,
// iteration, and cancellation errors return no partial history.
//
// The window is deliberately anchored to the NEWEST rows. A naive
// `ORDER BY timestamp ASC LIMIT maxMessages` returns the OLDEST rows,
// so once the active set exceeds maxMessages every newer message falls
// outside the window and the model's context silently freezes at that
// point (the amnesia bug). Compaction is token-gated and does not bound
// the active message count, so a long run of short messages can push
// the count past maxMessages while staying under the token threshold —
// hence the newest-N window is load-bearing, not just tidy.
//
// Compaction summaries carry the compacted region's earliest timestamp
// (ApplyCompaction), so they sort at the head of history and would fall
// outside a naive newest-N window; the UNION arm force-includes them so
// the model never loses its compacted past. UNION (set semantics) also
// dedupes a summary that happens to land inside the newest-N window.
func (s *SQLiteStore) GetMessages(ctx context.Context, conversationID string) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH recent AS (
			SELECT id, role, content, timestamp, COALESCE(mid_turn, 0) AS mid_turn, COALESCE(origin, '') AS origin
			FROM messages
			WHERE conversation_id = ? AND status = 'active'
			ORDER BY timestamp DESC, id DESC
			LIMIT ?
		)
		SELECT id, role, content, timestamp, mid_turn, origin FROM recent
		UNION
		SELECT id, role, content, timestamp, COALESCE(mid_turn, 0), COALESCE(origin, '')
		FROM messages
		WHERE conversation_id = ? AND status = 'active' AND role = 'system'
		  AND content LIKE ? || '%'
		ORDER BY timestamp ASC, id ASC
	`, conversationID, s.maxMessages, conversationID, CompactionSummaryPrefix)
	if err != nil {
		return nil, fmt.Errorf("query active messages: %w", err)
	}
	defer rows.Close()

	messages, err := scanWorkingMessages(ctx, rows)
	if err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close active messages: %w", err)
	}

	// Observability: a read may have clipped older active rows. Clipping is
	// only possible when the window came back saturated — a result smaller
	// than maxMessages means the whole active set fit (active > maxMessages
	// would have filled the newest-N window), so skip the COUNT entirely in
	// that common case. When it could have clipped, confirm with a
	// best-effort COUNT and warn (rate-limited); the COUNT never blocks
	// history retrieval. A silent clip is exactly what hid the amnesia bug.
	if len(messages) >= s.maxMessages {
		var activeTotal int
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM messages
			WHERE conversation_id = ? AND status = 'active'
		`, conversationID).Scan(&activeTotal); err != nil {
			s.logger.Debug("could not count clipped active messages", "conversation_id", conversationID, "error", err)
		} else if activeTotal > len(messages) {
			s.maybeWarnClip(conversationID, activeTotal, len(messages))
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

// maybeWarnClip emits a rate-limited warning when GetMessages returned
// fewer rows than the conversation's active set — i.e. the read window
// clipped older active messages. Throttled per-conversation so a hot
// overflowing conversation (read every turn) does not spam the log.
func (s *SQLiteStore) maybeWarnClip(conversationID string, activeTotal, returned int) {
	if s.logger == nil {
		return
	}

	s.clipWarnMu.Lock()
	if last, ok := s.clipWarnAt[conversationID]; ok && time.Since(last) < 5*time.Minute {
		s.clipWarnMu.Unlock()
		return
	}
	s.clipWarnAt[conversationID] = time.Now()
	s.clipWarnMu.Unlock()

	s.logger.Warn("working-memory read window clipped older active messages",
		"conversation_id", conversationID,
		"active_total", activeTotal,
		"returned", returned,
		"max_messages", s.maxMessages,
	)
}

// ActiveMessageCount returns the number of active non-system messages in
// a conversation — the reducible set compaction can actually shrink
// (GetMessagesForCompaction filters role != 'system' too). Feeds the
// count-aware compaction trigger so a summary-only overflow can't spin
// an unsatisfiable compaction loop. A failed or canceled read returns an
// error rather than a zero count.
func (s *SQLiteStore) ActiveMessageCount(ctx context.Context, conversationID string) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM messages
		WHERE conversation_id = ? AND status = 'active' AND role != 'system'
	`, conversationID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active messages: %w", err)
	}
	return count, nil
}

// GetConversation retrieves a conversation with active history. Absence returns
// nil without an error; failed or canceled reads return no partial conversation.
func (s *SQLiteStore) GetConversation(ctx context.Context, id string) (*Conversation, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, created_at, updated_at, metadata FROM conversations WHERE id = ?
	`, id)

	var conv Conversation
	var createdAt, updatedAt string
	var metadata sql.NullString
	if err := row.Scan(&conv.ID, &createdAt, &updatedAt, &metadata); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query conversation: %w", err)
	}
	var err error
	if conv.CreatedAt, err = database.ParseTimestamp(createdAt); err != nil {
		return nil, fmt.Errorf("parse conversation created_at: %w", err)
	}
	if conv.UpdatedAt, err = database.ParseTimestamp(updatedAt); err != nil {
		return nil, fmt.Errorf("parse conversation updated_at: %w", err)
	}
	if metadata.Valid {
		if conv.Metadata, err = parseConversationMetadata(metadata.String); err != nil {
			return nil, fmt.Errorf("parse conversation metadata: %w", err)
		}
	}
	conv.Messages, err = s.GetMessages(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("read conversation history: %w", err)
	}
	return &conv, nil
}

// Clear empties the active conversation window while preserving its durable
// transcript and conversation metadata. Session-aware callers should use
// [SessionLifecycle] so the boundary and row ownership commit together.
func (s *SQLiteStore) Clear(conversationID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	_, err = tx.Exec(`UPDATE messages SET status = 'archived', archived_at = ?, archive_reason = 'clear'
		WHERE conversation_id = ? AND status IN ('active', 'compacted')`, now, conversationID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`UPDATE tool_calls SET status = 'archived', archived_at = ?
		WHERE conversation_id = ? AND status = 'active'`, now, conversationID)
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Evict the clip-warning rate-limit entry: delegate conversations use a
	// fresh unique ID per invocation and Clear on finish, so without this
	// the map would grow one stale entry per overflowing delegate for the
	// life of the process.
	s.forgetClipWarning(conversationID)

	return nil
}

func (s *SQLiteStore) forgetClipWarning(conversationID string) {
	s.clipWarnMu.Lock()
	delete(s.clipWarnAt, conversationID)
	s.clipWarnMu.Unlock()
}

// Stats returns memory statistics.
func (s *SQLiteStore) Stats() map[string]any {
	var convCount, msgCount, tokenCount int

	_ = s.db.QueryRow(`SELECT COUNT(*) FROM conversations`).Scan(&convCount)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE status = 'active'`).Scan(&msgCount)
	_ = s.db.QueryRow(`SELECT COALESCE(SUM(token_count), 0) FROM messages WHERE status = 'active'`).Scan(&tokenCount)

	return map[string]any{
		"conversations": convCount,
		"messages":      msgCount,
		"total_tokens":  tokenCount,
		"max_per_conv":  s.maxMessages,
		"storage":       "sqlite",
	}
}

// GetAllConversations returns diagnostic snapshots of all conversations and
// their active working-memory windows, as defined by [SQLiteStore.GetMessages].
// Query, decoding, or cancellation errors return no partial snapshot.
func (s *SQLiteStore) GetAllConversations(ctx context.Context) ([]*Conversation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, created_at, updated_at, metadata FROM conversations ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("query snapshot conversations: %w", err)
	}
	defer rows.Close()

	var convs []*Conversation
	for rows.Next() {
		var conv Conversation
		var createdAt, updatedAt string
		var metadata sql.NullString
		if err := rows.Scan(&conv.ID, &createdAt, &updatedAt, &metadata); err != nil {
			return nil, fmt.Errorf("scan snapshot conversation: %w", err)
		}
		if metadata.Valid {
			conv.Metadata, err = parseConversationMetadata(metadata.String)
			if err != nil {
				return nil, fmt.Errorf("parse snapshot conversation metadata: %w", err)
			}
		}
		if conv.CreatedAt, err = database.ParseTimestamp(createdAt); err != nil {
			return nil, fmt.Errorf("parse snapshot conversation created_at: %w", err)
		}
		if conv.UpdatedAt, err = database.ParseTimestamp(updatedAt); err != nil {
			return nil, fmt.Errorf("parse snapshot conversation updated_at: %w", err)
		}
		convs = append(convs, &conv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read snapshot conversations: %w", err)
	}
	// Do not hold the metadata query's connection while loading history;
	// callers may use a single-connection database.
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close snapshot conversations: %w", err)
	}
	for _, conv := range convs {
		conv.Messages, err = s.GetMessages(ctx, conv.ID)
		if err != nil {
			return nil, fmt.Errorf("read snapshot conversation %s: %w", conv.ID, err)
		}
	}
	return convs, nil
}

// PutConversationMetadata replaces the typed metadata for a
// conversation, creating the conversation row if needed.
func (s *SQLiteStore) PutConversationMetadata(conversationID string, metadata *ConversationMetadata) error {
	if _, err := s.GetOrCreateConversation(conversationID); err != nil {
		return err
	}
	raw, err := marshalConversationMetadata(metadata)
	if err != nil {
		return fmt.Errorf("marshal conversation metadata: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`
		UPDATE conversations
		SET metadata = ?, updated_at = ?
		WHERE id = ?
	`, raw, now, conversationID)
	if err != nil {
		return fmt.Errorf("update conversation metadata: %w", err)
	}
	return nil
}

// BindConversationChannel updates only the channel-binding
// portion of a conversation's typed metadata.
func (s *SQLiteStore) BindConversationChannel(conversationID string, binding *ChannelBinding) error {
	var metadata *ConversationMetadata
	conv, err := s.GetConversation(context.Background(), conversationID)
	if err != nil {
		return fmt.Errorf("read conversation before binding channel: %w", err)
	}
	if conv != nil && conv.Metadata != nil {
		metadata = conv.Metadata.Clone()
	}
	if metadata == nil {
		metadata = &ConversationMetadata{}
	}
	metadata.ChannelBinding = binding.Normalize()
	return s.PutConversationMetadata(conversationID, metadata)
}

// GetAllMessages retrieves ALL messages for a conversation, including compacted ones.
// Includes tool call data for full-fidelity archiving — never lose primary sources.
// Failed or canceled reads return an error without partial history.
func (s *SQLiteStore) GetAllMessages(ctx context.Context, conversationID string) ([]Message, error) {
	return s.readLifecycleMessages(ctx, conversationID, false)
}

// CurrentSessionMessages returns the current non-archived rows, including
// compacted source rows, in stable chronological order. Archived history is
// excluded so a retroactive split cannot reach into a previous session.
func (s *SQLiteStore) CurrentSessionMessages(conversationID string) ([]Message, error) {
	return s.readLifecycleMessages(context.Background(), conversationID, true)
}

func (s *SQLiteStore) readLifecycleMessages(ctx context.Context, conversationID string, currentOnly bool) ([]Message, error) {
	condition := ""
	if currentOnly {
		condition = " AND status IN ('active', 'compacted')"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, role, content, timestamp, tool_calls, tool_call_id, COALESCE(mid_turn, 0), COALESCE(origin, '')
		FROM messages
		WHERE conversation_id = ?`+condition+`
		ORDER BY timestamp ASC, id ASC
	`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("query conversation messages: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var m Message
		var toolCalls, toolCallID sql.NullString
		var midTurn int
		var timestamp string
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &timestamp, &toolCalls, &toolCallID, &midTurn, &m.Origin); err != nil {
			return nil, fmt.Errorf("scan conversation message: %w", err)
		}
		if m.Timestamp, err = database.ParseTimestamp(timestamp); err != nil {
			return nil, fmt.Errorf("parse conversation message timestamp: %w", err)
		}
		if toolCalls.Valid {
			m.ToolCalls = toolCalls.String
		}
		if toolCallID.Valid {
			m.ToolCallID = toolCallID.String
		}
		m.MidTurn = midTurn != 0
		messages = append(messages, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read conversation messages: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

// GetTokenCount returns the active conversation's token count. An unreadable
// count returns an error rather than an apparently empty conversation.
func (s *SQLiteStore) GetTokenCount(ctx context.Context, conversationID string) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(token_count), 0)
		FROM messages
		WHERE conversation_id = ? AND status = 'active'
	`, conversationID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active conversation tokens: %w", err)
	}
	return count, nil
}

// GetMessagesForCompaction returns older active non-system messages, keeping
// the most recent keep messages. Errors return no partial selection.
func (s *SQLiteStore) GetMessagesForCompaction(ctx context.Context, conversationID string, keep int) ([]Message, error) {
	total, err := s.ActiveMessageCount(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if total <= keep {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, role, content, timestamp, COALESCE(mid_turn, 0), COALESCE(origin, '')
		FROM messages
		WHERE conversation_id = ? AND status = 'active' AND role != 'system'
		ORDER BY timestamp ASC, id ASC LIMIT ?
	`, conversationID, total-keep)
	if err != nil {
		return nil, fmt.Errorf("query compaction messages: %w", err)
	}
	defer rows.Close()
	return scanWorkingMessages(ctx, rows)
}

// GetActiveCompactionSummaries returns active compaction-summary system
// messages, oldest first. Query, decoding, and cancellation errors return no
// partial result, so a failed read cannot masquerade as no prior summaries.
func (s *SQLiteStore) GetActiveCompactionSummaries(ctx context.Context, conversationID string) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, role, content, timestamp, COALESCE(mid_turn, 0), COALESCE(origin, '')
		FROM messages
		WHERE conversation_id = ? AND status = 'active' AND role = 'system'
		  AND content LIKE ? || '%'
		ORDER BY timestamp ASC, id ASC
	`, conversationID, CompactionSummaryPrefix)
	if err != nil {
		return nil, fmt.Errorf("query compaction summaries: %w", err)
	}
	defer rows.Close()
	return scanWorkingMessages(ctx, rows)
}

// Required prompt and compaction reads share one strict scanner so a damaged
// row or interrupted iteration cannot silently remove part of the history.
func scanWorkingMessages(ctx context.Context, rows *sql.Rows) ([]Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var messages []Message
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var m Message
		var timestamp string
		var midTurn int
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &timestamp, &midTurn, &m.Origin); err != nil {
			return nil, fmt.Errorf("scan active message: %w", err)
		}
		var err error
		if m.Timestamp, err = database.ParseTimestamp(timestamp); err != nil {
			return nil, fmt.Errorf("parse active message timestamp: %w", err)
		}
		m.MidTurn = midTurn != 0
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read active messages: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

// ApplyCompaction atomically marks the given messages compacted and
// inserts the replacement summary at summaryTS, in one transaction.
// Splitting these into two writes risks losing active history if the
// insert fails after the mark (and, with summary folding, could drop
// the conversation's only summary) — so they commit or roll back
// together. Caller cancellation aborts the transaction rather than applying a
// summary produced after its request ended.
func (s *SQLiteStore) ApplyCompaction(ctx context.Context, conversationID string, compactedIDs []string, summary string, summaryTS time.Time) error {
	msgID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate summary ID: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin compaction tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if len(compactedIDs) > 0 {
		args := make([]any, 0, len(compactedIDs)+1)
		args = append(args, conversationID)
		for _, id := range compactedIDs {
			args = append(args, id)
		}
		result, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE messages
			SET status = 'compacted'
			WHERE conversation_id = ? AND status = 'active' AND id IN (%s)
		`, database.Placeholders(len(compactedIDs))), args...)
		if err != nil {
			return fmt.Errorf("mark compacted: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count compacted messages: %w", err)
		}
		if affected != int64(len(compactedIDs)) {
			return fmt.Errorf("conversation changed during compaction; preserved the current session without applying a stale summary")
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count, status, origin)
		VALUES (?, ?, 'system', ?, ?, ?, 'active', ?)
	`, msgID.String(), conversationID, summary, summaryTS, llm.EstimateTokens(summary), OriginInternal); err != nil {
		return fmt.Errorf("insert summary: %w", err)
	}

	return tx.Commit()
}

// AddCompactionSummary adds a compaction summary message stamped now.
// Used for session handoffs and other unpositioned system notes; the
// compactor itself uses ApplyCompaction to place the summary at the
// compacted region's position atomically with the mark.
func (s *SQLiteStore) AddCompactionSummary(conversationID, summary string) error {
	msgID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate summary ID: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count, status, origin)
		VALUES (?, ?, 'system', ?, ?, ?, 'active', ?)
	`, msgID.String(), conversationID, summary, time.Now(), llm.EstimateTokens(summary), OriginInternal)

	return err
}

// ToolCall represents a recorded tool invocation.
type ToolCall struct {
	ID             string     `json:"id"`
	MessageID      string     `json:"message_id"`
	ConversationID string     `json:"conversation_id"`
	ToolName       string     `json:"tool_name"`
	Arguments      string     `json:"arguments"`
	Result         string     `json:"result,omitempty"`
	Error          string     `json:"error,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	DurationMs     int64      `json:"duration_ms,omitempty"`
}

// RecordToolCall records a tool call execution.
// messageID can be empty - it will be stored as NULL.
func (s *SQLiteStore) RecordToolCall(conversationID, messageID, toolCallID, toolName, arguments string) error {
	return s.RecordSessionToolCall(conversationID, "", messageID, toolCallID, toolName, arguments)
}

// RecordSessionToolCall records a tool call against the session that produced
// its model iteration. A later call in the same response can arrive after an
// earlier call closed that session; it is then inserted as archived so it
// cannot leak into the successor's active window. A non-empty sessionID must
// belong to conversationID. Empty sessionID retains deferred session claiming.
func (s *SQLiteStore) RecordSessionToolCall(conversationID, sessionID, messageID, toolCallID, toolName, arguments string) error {
	now := time.Now()

	var msgID any
	if messageID != "" {
		msgID = messageID
	} // else nil (NULL)
	if sessionID != "" {
		result, err := s.db.Exec(`INSERT INTO tool_calls
			(id, message_id, conversation_id, session_id, tool_name, arguments, started_at, status, archived_at)
			SELECT ?, ?, ?, id, ?, ?, ?,
				CASE WHEN ended_at IS NULL THEN 'active' ELSE 'archived' END,
				CASE WHEN ended_at IS NULL THEN NULL ELSE ? END
			FROM sessions WHERE id = ? AND conversation_id = ?`,
			toolCallID, msgID, conversationID, toolName, arguments, now, now, sessionID, conversationID)
		if err != nil {
			return fmt.Errorf("record session tool call: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count recorded session tool call: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("tool call session %s does not belong to conversation %s", sessionID, conversationID)
		}
		return nil
	}

	_, err := s.db.Exec(`
		INSERT INTO tool_calls (id, message_id, conversation_id, tool_name, arguments, started_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, toolCallID, msgID, conversationID, toolName, arguments, now)

	return err
}

// CompleteToolCall records the result of a tool call.
func (s *SQLiteStore) CompleteToolCall(toolCallID, result, errMsg string) error {
	now := time.Now()

	// Get started_at to calculate duration
	var startedAt time.Time
	err := s.db.QueryRow(`SELECT started_at FROM tool_calls WHERE id = ?`, toolCallID).Scan(&startedAt)
	if err != nil {
		return fmt.Errorf("tool call not found: %s", toolCallID)
	}

	durationMs := now.Sub(startedAt).Milliseconds()

	_, err = s.db.Exec(`
		UPDATE tool_calls 
		SET result = ?, error = ?, completed_at = ?, duration_ms = ?
		WHERE id = ?
	`, result, errMsg, now, durationMs, toolCallID)

	return err
}

// GetToolCalls retrieves tool calls, optionally filtered by conversation.
// If conversationID is empty, returns all recent tool calls.
func (s *SQLiteStore) GetToolCalls(conversationID string, limit int) []ToolCall {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000 // Cap to prevent memory exhaustion
	}

	var rows *sql.Rows
	var err error

	if conversationID != "" {
		rows, err = s.db.Query(`
			SELECT id, message_id, conversation_id, tool_name, arguments,
			       result, error, started_at, completed_at, duration_ms
			FROM tool_calls
			WHERE conversation_id = ? AND status = 'active'
			ORDER BY started_at DESC
			LIMIT ?
		`, conversationID, limit)
	} else {
		// No filter - get all recent active tool calls.
		rows, err = s.db.Query(`
			SELECT id, message_id, conversation_id, tool_name, arguments,
			       result, error, started_at, completed_at, duration_ms
			FROM tool_calls
			WHERE status = 'active'
			ORDER BY started_at DESC
			LIMIT ?
		`, limit)
	}
	if err != nil {
		return nil
	}
	defer rows.Close()

	var calls []ToolCall
	for rows.Next() {
		var tc ToolCall
		var messageID, result, errMsg sql.NullString
		var completedAt sql.NullTime
		var durationMs sql.NullInt64

		err := rows.Scan(&tc.ID, &messageID, &tc.ConversationID, &tc.ToolName,
			&tc.Arguments, &result, &errMsg, &tc.StartedAt, &completedAt, &durationMs)
		if err != nil {
			continue
		}

		if messageID.Valid {
			tc.MessageID = messageID.String
		}
		if result.Valid {
			tc.Result = result.String
		}
		if errMsg.Valid {
			tc.Error = errMsg.String
		}
		if completedAt.Valid {
			tc.CompletedAt = &completedAt.Time
		}
		if durationMs.Valid {
			tc.DurationMs = durationMs.Int64
		}

		calls = append(calls, tc)
	}

	return calls
}

// ClearToolCalls deletes tool call records for a conversation from the
// working store. Called after archiving to prevent re-archival on the
// next session split.
func (s *SQLiteStore) ClearToolCalls(conversationID string) error {
	if conversationID == "" {
		return fmt.Errorf("conversation ID required for ClearToolCalls")
	}
	_, err := s.db.Exec(`DELETE FROM tool_calls WHERE conversation_id = ?`, conversationID)
	return err
}

// GetToolCallsByName retrieves tool calls filtered by tool name.
func (s *SQLiteStore) GetToolCallsByName(toolName string, limit int) []ToolCall {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000 // Cap to prevent memory exhaustion
	}

	rows, err := s.db.Query(`
		SELECT id, message_id, conversation_id, tool_name, arguments,
		       result, error, started_at, completed_at, duration_ms
		FROM tool_calls
		WHERE tool_name = ? AND status = 'active'
		ORDER BY started_at DESC
		LIMIT ?
	`, toolName, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var calls []ToolCall
	for rows.Next() {
		var tc ToolCall
		var messageID, result, errMsg sql.NullString
		var completedAt sql.NullTime
		var durationMs sql.NullInt64

		err := rows.Scan(&tc.ID, &messageID, &tc.ConversationID, &tc.ToolName,
			&tc.Arguments, &result, &errMsg, &tc.StartedAt, &completedAt, &durationMs)
		if err != nil {
			continue
		}

		if messageID.Valid {
			tc.MessageID = messageID.String
		}
		if result.Valid {
			tc.Result = result.String
		}
		if errMsg.Valid {
			tc.Error = errMsg.String
		}
		if completedAt.Valid {
			tc.CompletedAt = &completedAt.Time
		}
		if durationMs.Valid {
			tc.DurationMs = durationMs.Int64
		}

		calls = append(calls, tc)
	}

	return calls
}

// ToolCallStats returns statistics about tool usage.
func (s *SQLiteStore) ToolCallStats() map[string]any {
	stats := make(map[string]any)

	// Total calls
	var total int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&total)
	stats["total_calls"] = total

	// By tool
	byTool := make(map[string]int)
	rows, err := s.db.Query(`SELECT tool_name, COUNT(*) FROM tool_calls GROUP BY tool_name ORDER BY COUNT(*) DESC`)
	if err == nil && rows != nil {
		defer rows.Close()
		for rows.Next() {
			var name string
			var count int
			if err := rows.Scan(&name, &count); err != nil {
				continue // Skip malformed rows
			}
			byTool[name] = count
		}
	}
	stats["by_tool"] = byTool

	// Average duration
	var avgMs float64
	_ = s.db.QueryRow(`SELECT COALESCE(AVG(duration_ms), 0) FROM tool_calls WHERE completed_at IS NOT NULL`).Scan(&avgMs)
	stats["avg_duration_ms"] = avgMs

	// Error rate
	var errors int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE error IS NOT NULL AND error != ''`).Scan(&errors)
	if total > 0 {
		stats["error_rate"] = float64(errors) / float64(total)
	} else {
		stats["error_rate"] = 0.0
	}

	return stats
}
