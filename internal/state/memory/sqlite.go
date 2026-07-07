package memory

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
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

// AddMessage adds a message to a conversation.
func (s *SQLiteStore) AddMessage(conversationID, role, content string) error {
	return s.addMessage(conversationID, role, content, false)
}

// AddMidTurnMessage adds a message that arrived mid-turn and was merged into
// an in-flight turn (#1230), tagging the row so consumers can identify the
// injection from the structured record rather than substring-matching the
// channel-rendered arrival marker in the content.
func (s *SQLiteStore) AddMidTurnMessage(conversationID, role, content string) error {
	return s.addMessage(conversationID, role, content, true)
}

func (s *SQLiteStore) addMessage(conversationID, role, content string, midTurn bool) error {
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
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count, mid_turn)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, msgID.String(), conversationID, role, content, now, llm.EstimateTokens(content), midTurnVal)
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
// summary, returned in chronological (ASC) order.
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
func (s *SQLiteStore) GetMessages(conversationID string) []Message {
	rows, err := s.db.Query(`
		WITH recent AS (
			SELECT id, role, content, timestamp, COALESCE(mid_turn, 0) AS mid_turn
			FROM messages
			WHERE conversation_id = ? AND status = 'active'
			ORDER BY timestamp DESC, id DESC
			LIMIT ?
		)
		SELECT id, role, content, timestamp, mid_turn FROM recent
		UNION
		SELECT id, role, content, timestamp, COALESCE(mid_turn, 0)
		FROM messages
		WHERE conversation_id = ? AND status = 'active' AND role = 'system'
		  AND content LIKE ? || '%'
		ORDER BY timestamp ASC, id ASC
	`, conversationID, s.maxMessages, conversationID, CompactionSummaryPrefix)
	if err != nil {
		return []Message{}
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var m Message
		var midTurn int
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.Timestamp, &midTurn); err != nil {
			continue
		}
		m.MidTurn = midTurn != 0
		messages = append(messages, m)
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
		_ = s.db.QueryRow(`
			SELECT COUNT(*) FROM messages
			WHERE conversation_id = ? AND status = 'active'
		`, conversationID).Scan(&activeTotal)
		if activeTotal > len(messages) {
			s.maybeWarnClip(conversationID, activeTotal, len(messages))
		}
	}

	return messages
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
// an unsatisfiable compaction loop.
func (s *SQLiteStore) ActiveMessageCount(conversationID string) int {
	var count int
	_ = s.db.QueryRow(`
		SELECT COUNT(*)
		FROM messages
		WHERE conversation_id = ? AND status = 'active' AND role != 'system'
	`, conversationID).Scan(&count)
	return count
}

// GetConversation retrieves a conversation by ID.
func (s *SQLiteStore) GetConversation(id string) *Conversation {
	row := s.db.QueryRow(`
		SELECT id, created_at, updated_at, metadata FROM conversations WHERE id = ?
	`, id)

	var conv Conversation
	var metadata sql.NullString
	if err := row.Scan(&conv.ID, &conv.CreatedAt, &conv.UpdatedAt, &metadata); err != nil {
		return nil
	}
	if metadata.Valid {
		meta, err := parseConversationMetadata(metadata.String)
		if err != nil {
			s.logger.Warn("conversation metadata invalid",
				"conversation_id", id,
				"error", err,
			)
		} else {
			conv.Metadata = meta
		}
	}

	conv.Messages = s.GetMessages(id)
	return &conv
}

// Clear removes a conversation and its messages.
func (s *SQLiteStore) Clear(conversationID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.Exec(`DELETE FROM messages WHERE conversation_id = ?`, conversationID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`DELETE FROM conversations WHERE id = ?`, conversationID)
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
	s.clipWarnMu.Lock()
	delete(s.clipWarnAt, conversationID)
	s.clipWarnMu.Unlock()

	return nil
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

// GetAllConversations returns all conversations for checkpointing.
func (s *SQLiteStore) GetAllConversations() []*Conversation {
	rows, err := s.db.Query(`
		SELECT id, created_at, updated_at, metadata FROM conversations ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var convs []*Conversation
	for rows.Next() {
		var id, createdAt, updatedAt string
		var metadata sql.NullString
		if err := rows.Scan(&id, &createdAt, &updatedAt, &metadata); err != nil {
			continue
		}

		conv := &Conversation{
			ID:       id,
			Messages: s.GetMessages(id),
		}
		if metadata.Valid {
			if meta, err := parseConversationMetadata(metadata.String); err != nil {
				s.logger.Warn("conversation metadata invalid during snapshot",
					"conversation_id", id,
					"error", err,
				)
			} else {
				conv.Metadata = meta
			}
		}
		if t, err := database.ParseTimestamp(createdAt); err != nil {
			s.logger.Warn("conversation created_at invalid during snapshot",
				"conversation_id", id, "created_at", createdAt, "error", err)
		} else {
			conv.CreatedAt = t
		}
		if t, err := database.ParseTimestamp(updatedAt); err != nil {
			s.logger.Warn("conversation updated_at invalid during snapshot",
				"conversation_id", id, "updated_at", updatedAt, "error", err)
		} else {
			conv.UpdatedAt = t
		}

		convs = append(convs, conv)
	}
	return convs
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
	if conv := s.GetConversation(conversationID); conv != nil && conv.Metadata != nil {
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
func (s *SQLiteStore) GetAllMessages(conversationID string) []Message {
	rows, err := s.db.Query(`
		SELECT id, role, content, timestamp, tool_calls, tool_call_id, COALESCE(mid_turn, 0)
		FROM messages
		WHERE conversation_id = ?
		ORDER BY timestamp ASC
	`, conversationID)
	if err != nil {
		return []Message{}
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var m Message
		var toolCalls, toolCallID sql.NullString
		var midTurn int
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.Timestamp, &toolCalls, &toolCallID, &midTurn); err != nil {
			continue
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

	return messages
}

// GetTokenCount returns the total token count for a conversation.
func (s *SQLiteStore) GetTokenCount(conversationID string) int {
	var count int
	_ = s.db.QueryRow(`
		SELECT COALESCE(SUM(token_count), 0)
		FROM messages
		WHERE conversation_id = ? AND status = 'active'
	`, conversationID).Scan(&count)
	return count
}

// NeedsCompaction checks if a conversation needs compaction.
func (s *SQLiteStore) NeedsCompaction(conversationID string, maxTokens int) bool {
	return s.GetTokenCount(conversationID) > int(float64(maxTokens)*0.7)
}

// GetMessagesForCompaction retrieves messages that should be compacted.
// Keeps the most recent 'keep' messages.
func (s *SQLiteStore) GetMessagesForCompaction(conversationID string, keep int) []Message {
	// Get total count
	var total int
	_ = s.db.QueryRow(`
		SELECT COUNT(*) FROM messages
		WHERE conversation_id = ? AND status = 'active' AND role != 'system'
	`, conversationID).Scan(&total)

	if total <= keep {
		return nil // Nothing to compact
	}

	// Get older messages (everything except the last 'keep')
	offset := 0
	limit := total - keep

	rows, err := s.db.Query(`
		SELECT id, role, content, timestamp
		FROM messages
		WHERE conversation_id = ? AND status = 'active' AND role != 'system'
		ORDER BY timestamp ASC
		LIMIT ? OFFSET ?
	`, conversationID, limit, offset)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.Timestamp); err != nil {
			continue
		}
		messages = append(messages, m)
	}

	return messages
}

// GetActiveCompactionSummaries returns the active compaction-summary
// system messages for a conversation, oldest first. The prefix match
// distinguishes summaries from other system rows (session handoffs,
// notices) without a schema change. A query error is surfaced so the
// caller can abort rather than mistake a transient failure for "no
// prior summaries" and stack a fresh one.
func (s *SQLiteStore) GetActiveCompactionSummaries(conversationID string) ([]Message, error) {
	rows, err := s.db.Query(`
		SELECT id, role, content, timestamp
		FROM messages
		WHERE conversation_id = ? AND status = 'active' AND role = 'system'
		  AND content LIKE ? || '%'
		ORDER BY timestamp ASC
	`, conversationID, CompactionSummaryPrefix)
	if err != nil {
		return nil, fmt.Errorf("query compaction summaries: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.Timestamp); err != nil {
			return nil, fmt.Errorf("scan compaction summary: %w", err)
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// ApplyCompaction atomically marks the given messages compacted and
// inserts the replacement summary at summaryTS, in one transaction.
// Splitting these into two writes risks losing active history if the
// insert fails after the mark (and, with summary folding, could drop
// the conversation's only summary) — so they commit or roll back
// together.
func (s *SQLiteStore) ApplyCompaction(conversationID string, compactedIDs []string, summary string, summaryTS time.Time) error {
	msgID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate summary ID: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin compaction tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if len(compactedIDs) > 0 {
		placeholders := make([]string, len(compactedIDs))
		args := make([]any, 0, len(compactedIDs)+1)
		args = append(args, conversationID)
		for i, id := range compactedIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		if _, err := tx.Exec(fmt.Sprintf(`
			UPDATE messages
			SET status = 'compacted'
			WHERE conversation_id = ? AND id IN (%s)
		`, strings.Join(placeholders, ",")), args...); err != nil {
			return fmt.Errorf("mark compacted: %w", err)
		}
	}

	if _, err := tx.Exec(`
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count, status)
		VALUES (?, ?, 'system', ?, ?, ?, 'active')
	`, msgID.String(), conversationID, summary, summaryTS, llm.EstimateTokens(summary)); err != nil {
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
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count, status)
		VALUES (?, ?, 'system', ?, ?, ?, 'active')
	`, msgID.String(), conversationID, summary, time.Now(), llm.EstimateTokens(summary))

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
	now := time.Now()

	var msgID any
	if messageID != "" {
		msgID = messageID
	} // else nil (NULL)

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

// ArchiveToolCalls updates tool calls in the unified table to archived status.
// This replaces the cross-DB copy that the legacy archive flow used.
func (s *SQLiteStore) ArchiveToolCalls(conversationID, sessionID string) (int64, error) {
	now := time.Now().UTC()
	result, err := s.db.Exec(`
		UPDATE tool_calls
		SET session_id = COALESCE(session_id, ?),
		    status = 'archived',
		    archived_at = ?
		WHERE conversation_id = ? AND status = 'active'
	`, sessionID, now.Format(time.RFC3339Nano), conversationID)
	if err != nil {
		return 0, fmt.Errorf("archive tool calls: %w", err)
	}
	return result.RowsAffected()
}

// ArchiveMessages updates messages in the unified table to archived status.
// This replaces the cross-DB copy that the legacy archive flow used.
func (s *SQLiteStore) ArchiveMessages(conversationID, sessionID, reason string) (int64, error) {
	now := time.Now().UTC()
	result, err := s.db.Exec(`
		UPDATE messages
		SET session_id = COALESCE(session_id, ?),
		    status = 'archived',
		    archived_at = ?,
		    archive_reason = ?
		WHERE conversation_id = ? AND status IN ('active', 'compacted')
	`, sessionID, now.Format(time.RFC3339Nano), reason, conversationID)
	if err != nil {
		return 0, fmt.Errorf("archive messages: %w", err)
	}
	return result.RowsAffected()
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
