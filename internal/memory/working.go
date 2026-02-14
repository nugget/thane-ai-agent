package memory

import (
	"database/sql"
	"fmt"
	"time"
)

// WorkingMemoryStore persists free-form working memory per conversation.
// Working memory captures experiential context that mechanical
// summarisation destroys: emotional tone, conversational arc,
// relationship temperature, and unresolved threads. The table lives in
// archive.db alongside session transcripts.
type WorkingMemoryStore struct {
	db *sql.DB
}

// NewWorkingMemoryStore creates a working memory store using the given
// database connection (typically from [ArchiveStore.DB]). It creates the
// working_memory table if it does not already exist.
func NewWorkingMemoryStore(db *sql.DB) (*WorkingMemoryStore, error) {
	s := &WorkingMemoryStore{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("working memory migration: %w", err)
	}
	return s, nil
}

// migrate creates the working_memory table if it does not exist.
func (s *WorkingMemoryStore) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS working_memory (
			conversation_id TEXT NOT NULL PRIMARY KEY,
			content         TEXT NOT NULL,
			updated_at      TEXT NOT NULL
		)
	`)
	return err
}

// Get returns the working memory content and last-updated timestamp for
// a conversation. If no working memory exists, it returns an empty
// string and zero time with no error.
func (s *WorkingMemoryStore) Get(conversationID string) (string, time.Time, error) {
	var content string
	var updatedAtStr string

	err := s.db.QueryRow(`
		SELECT content, updated_at FROM working_memory
		WHERE conversation_id = ?
	`, conversationID).Scan(&content, &updatedAtStr)

	if err == sql.ErrNoRows {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("get working memory: %w", err)
	}

	updatedAt, _ := time.Parse(time.RFC3339Nano, updatedAtStr)
	return content, updatedAt, nil
}

// Set writes or replaces the working memory content for a conversation.
func (s *WorkingMemoryStore) Set(conversationID, content string) error {
	_, err := s.db.Exec(`
		INSERT INTO working_memory (conversation_id, content, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(conversation_id) DO UPDATE SET
			content = excluded.content,
			updated_at = excluded.updated_at
	`, conversationID, content, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("set working memory: %w", err)
	}
	return nil
}

// Delete removes the working memory for a conversation.
func (s *WorkingMemoryStore) Delete(conversationID string) error {
	_, err := s.db.Exec(`
		DELETE FROM working_memory WHERE conversation_id = ?
	`, conversationID)
	if err != nil {
		return fmt.Errorf("delete working memory: %w", err)
	}
	return nil
}
