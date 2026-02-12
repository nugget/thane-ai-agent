// Package memory provides conversation memory storage.
package memory

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

// SQLiteStore is a SQLite-backed memory store.
type SQLiteStore struct {
	db          *sql.DB
	maxMessages int
}

// NewSQLiteStore creates a new SQLite-backed store.
func NewSQLiteStore(dbPath string, maxMessages int) (*SQLiteStore, error) {
	if maxMessages <= 0 {
		maxMessages = 100
	}

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	store := &SQLiteStore{
		db:          db,
		maxMessages: maxMessages,
	}

	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return store, nil
}

// migrate creates the database schema.
func (s *SQLiteStore) migrate() error {
	schema := `
	-- Conversations
	CREATE TABLE IF NOT EXISTS conversations (
		id TEXT PRIMARY KEY,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		metadata TEXT
	);

	-- Messages
	CREATE TABLE IF NOT EXISTS messages (
		id TEXT PRIMARY KEY,
		conversation_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		timestamp TIMESTAMP NOT NULL,
		token_count INTEGER DEFAULT 0,
		compacted BOOLEAN DEFAULT FALSE,
		tool_calls TEXT,
		tool_call_id TEXT,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id, timestamp);
	CREATE INDEX IF NOT EXISTS idx_messages_compacted ON messages(conversation_id, compacted);

	-- Tool calls (structured, queryable)
	CREATE TABLE IF NOT EXISTS tool_calls (
		id TEXT PRIMARY KEY,
		message_id TEXT,
		conversation_id TEXT NOT NULL,
		tool_name TEXT NOT NULL,
		arguments TEXT NOT NULL,
		result TEXT,
		error TEXT,
		started_at TIMESTAMP NOT NULL,
		completed_at TIMESTAMP,
		duration_ms INTEGER,
		FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE SET NULL,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_tool_calls_conversation ON tool_calls(conversation_id, started_at);
	CREATE INDEX IF NOT EXISTS idx_tool_calls_tool ON tool_calls(tool_name);
	CREATE INDEX IF NOT EXISTS idx_tool_calls_message ON tool_calls(message_id);

	-- Entity facts (for Phase 3)
	CREATE TABLE IF NOT EXISTS entity_facts (
		id TEXT PRIMARY KEY,
		entity_id TEXT NOT NULL,
		fact_type TEXT NOT NULL,
		content TEXT NOT NULL,
		source TEXT NOT NULL,
		confidence REAL DEFAULT 1.0,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		valid_until TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_entity_facts_entity ON entity_facts(entity_id);

	-- Preferences (for Phase 5)
	CREATE TABLE IF NOT EXISTS preferences (
		id TEXT PRIMARY KEY,
		category TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT NOT NULL,
		context TEXT,
		confidence REAL DEFAULT 1.0,
		learned_from TEXT,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);

	-- Note: Events/state history NOT stored here
	-- Query Home Assistant's recorder/history APIs instead
	`

	_, err := s.db.Exec(schema)
	return err
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

	return &Conversation{
		ID:        id,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// AddMessage adds a message to a conversation.
func (s *SQLiteStore) AddMessage(conversationID, role, content string) error {
	now := time.Now()
	msgID, _ := uuid.NewV7()

	// Ensure conversation exists
	_, err := s.GetOrCreateConversation(conversationID)
	if err != nil {
		return err
	}

	// Insert message
	_, err = s.db.Exec(`
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count)
		VALUES (?, ?, ?, ?, ?, ?)
	`, msgID.String(), conversationID, role, content, now, estimateTokens(content))
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

// GetMessages retrieves messages for a conversation.
func (s *SQLiteStore) GetMessages(conversationID string) []Message {
	rows, err := s.db.Query(`
		SELECT role, content, timestamp
		FROM messages
		WHERE conversation_id = ? AND compacted = FALSE
		ORDER BY timestamp ASC
		LIMIT ?
	`, conversationID, s.maxMessages)
	if err != nil {
		return []Message{}
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Role, &m.Content, &m.Timestamp); err != nil {
			continue
		}
		messages = append(messages, m)
	}

	return messages
}

// GetConversation retrieves a conversation by ID.
func (s *SQLiteStore) GetConversation(id string) *Conversation {
	row := s.db.QueryRow(`
		SELECT id, created_at, updated_at FROM conversations WHERE id = ?
	`, id)

	var conv Conversation
	if err := row.Scan(&conv.ID, &conv.CreatedAt, &conv.UpdatedAt); err != nil {
		return nil
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

	return tx.Commit()
}

// Stats returns memory statistics.
func (s *SQLiteStore) Stats() map[string]any {
	var convCount, msgCount, tokenCount int

	_ = s.db.QueryRow(`SELECT COUNT(*) FROM conversations`).Scan(&convCount)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE compacted = FALSE`).Scan(&msgCount)
	_ = s.db.QueryRow(`SELECT COALESCE(SUM(token_count), 0) FROM messages WHERE compacted = FALSE`).Scan(&tokenCount)

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
		SELECT id, created_at, updated_at FROM conversations ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var convs []*Conversation
	for rows.Next() {
		var id, createdAt, updatedAt string
		if err := rows.Scan(&id, &createdAt, &updatedAt); err != nil {
			continue
		}

		conv := &Conversation{
			ID:       id,
			Messages: s.GetMessages(id),
		}
		conv.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		conv.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)

		convs = append(convs, conv)
	}
	return convs
}

// GetAllMessages retrieves ALL messages for a conversation, including compacted ones.
// Includes tool call data for full-fidelity archiving — never lose primary sources.
func (s *SQLiteStore) GetAllMessages(conversationID string) []Message {
	rows, err := s.db.Query(`
		SELECT role, content, timestamp, tool_calls, tool_call_id
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
		if err := rows.Scan(&m.Role, &m.Content, &m.Timestamp, &toolCalls, &toolCallID); err != nil {
			continue
		}
		if toolCalls.Valid {
			m.ToolCalls = toolCalls.String
		}
		if toolCallID.Valid {
			m.ToolCallID = toolCallID.String
		}
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
		WHERE conversation_id = ? AND compacted = FALSE
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
		WHERE conversation_id = ? AND compacted = FALSE AND role != 'system'
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
		WHERE conversation_id = ? AND compacted = FALSE AND role != 'system'
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
		var id string
		if err := rows.Scan(&id, &m.Role, &m.Content, &m.Timestamp); err != nil {
			continue
		}
		messages = append(messages, m)
	}

	return messages
}

// MarkCompacted marks messages as compacted.
func (s *SQLiteStore) MarkCompacted(conversationID string, before time.Time) error {
	_, err := s.db.Exec(`
		UPDATE messages 
		SET compacted = TRUE 
		WHERE conversation_id = ? AND timestamp < ? AND role != 'system'
	`, conversationID, before)
	return err
}

// AddCompactionSummary adds a compaction summary message.
func (s *SQLiteStore) AddCompactionSummary(conversationID, summary string) error {
	now := time.Now()
	msgID, _ := uuid.NewV7()

	_, err := s.db.Exec(`
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count, compacted)
		VALUES (?, ?, 'system', ?, ?, ?, FALSE)
	`, msgID.String(), conversationID, summary, now, estimateTokens(summary))

	return err
}

// estimateTokens provides a rough token count estimate.
// Rule of thumb: ~4 characters per token for English.
func estimateTokens(text string) int {
	return len(text) / 4
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
			WHERE conversation_id = ?
			ORDER BY started_at DESC
			LIMIT ?
		`, conversationID, limit)
	} else {
		// No filter - get all recent
		rows, err = s.db.Query(`
			SELECT id, message_id, conversation_id, tool_name, arguments, 
			       result, error, started_at, completed_at, duration_ms
			FROM tool_calls
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
		WHERE tool_name = ?
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
