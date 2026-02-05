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
		FOREIGN KEY (conversation_id) REFERENCES conversations(id)
	);
	CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id, timestamp);
	CREATE INDEX IF NOT EXISTS idx_messages_compacted ON messages(conversation_id, compacted);

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

	-- Events (for Phase 5)
	CREATE TABLE IF NOT EXISTS events (
		id TEXT PRIMARY KEY,
		event_type TEXT NOT NULL,
		entity_id TEXT,
		summary TEXT NOT NULL,
		details TEXT,
		timestamp TIMESTAMP NOT NULL,
		importance REAL DEFAULT 0.5
	);
	CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp DESC);
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
	msgID := uuid.New().String()

	// Ensure conversation exists
	_, err := s.GetOrCreateConversation(conversationID)
	if err != nil {
		return err
	}

	// Insert message
	_, err = s.db.Exec(`
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count)
		VALUES (?, ?, ?, ?, ?, ?)
	`, msgID, conversationID, role, content, now, estimateTokens(content))
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
	defer tx.Rollback()

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

	s.db.QueryRow(`SELECT COUNT(*) FROM conversations`).Scan(&convCount)
	s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE compacted = FALSE`).Scan(&msgCount)
	s.db.QueryRow(`SELECT COALESCE(SUM(token_count), 0) FROM messages WHERE compacted = FALSE`).Scan(&tokenCount)

	return map[string]any{
		"conversations":  convCount,
		"messages":       msgCount,
		"total_tokens":   tokenCount,
		"max_per_conv":   s.maxMessages,
		"storage":        "sqlite",
	}
}

// GetTokenCount returns the total token count for a conversation.
func (s *SQLiteStore) GetTokenCount(conversationID string) int {
	var count int
	s.db.QueryRow(`
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
	s.db.QueryRow(`
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
	msgID := uuid.New().String()

	_, err := s.db.Exec(`
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count, compacted)
		VALUES (?, ?, 'system', ?, ?, ?, FALSE)
	`, msgID, conversationID, summary, now, estimateTokens(summary))

	return err
}

// estimateTokens provides a rough token count estimate.
// Rule of thumb: ~4 characters per token for English.
func estimateTokens(text string) int {
	return len(text) / 4
}
