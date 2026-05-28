package memory

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// WorkingMemoryStore persists free-form working memory per conversation.
// Working memory captures experiential context that mechanical
// summarisation destroys: emotional tone, conversational arc,
// relationship temperature, and unresolved threads. The table lives in
// archive.db alongside session transcripts.
type WorkingMemoryStore struct {
	db         *sql.DB
	ftsEnabled bool
}

// NewWorkingMemoryStore creates a working memory store using the given
// database connection (typically from [ArchiveStore.DB]). It creates
// the working_memory table if it does not already exist. ftsEnabled
// should be the value returned by [ArchiveStore.FTSEnabled] on the
// archive store sharing this connection — when true, the constructor
// also sets up working_memory_fts with sync triggers and backfills
// any existing rows.
func NewWorkingMemoryStore(db *sql.DB, ftsEnabled bool) (*WorkingMemoryStore, error) {
	s := &WorkingMemoryStore{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("working memory migration: %w", err)
	}
	if ftsEnabled {
		s.ftsEnabled = trySetupWorkingMemoryFTS(db, ftsEnabled)
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

// FTSEnabled reports whether the working_memory_fts virtual table
// was successfully created at startup.
func (s *WorkingMemoryStore) FTSEnabled() bool {
	return s.ftsEnabled
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

	updatedAt, err := database.ParseTimestamp(updatedAtStr)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("parse working memory updated_at: %w", err)
	}
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

// Search runs an FTS5 query against working_memory_fts and returns
// the highest-ranking working-memory snapshots by BM25. Query is
// wrapped as a phrase token for the same precision reasons the raw
// archive search uses [phraseFTS5Query].
//
// Returns an empty slice when FTS5 isn't available, the query is
// blank, or no rows match. The shape mirrors [SessionMatch]: caller
// gets the conversation_id (which doubles as a foreign key into
// [WorkingMemoryStore.Get] for the full content), an updated_at
// timestamp, the content, and the snippet highlight.
func (s *WorkingMemoryStore) Search(query string, limit int) ([]WorkingMemoryMatch, error) {
	if !s.ftsEnabled {
		return nil, nil
	}
	q := phraseFTS5Query(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}

	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT w.conversation_id, w.content, w.updated_at,
		       snippet(%s, 0, '**', '**', '...', 32) AS highlight
		FROM %s
		JOIN working_memory w ON w.rowid = %s.rowid
		WHERE %s MATCH ?
		ORDER BY rank
		LIMIT ?
	`, workingMemoryFTSTable, workingMemoryFTSTable, workingMemoryFTSTable, workingMemoryFTSTable), q, limit)
	if err != nil {
		return nil, fmt.Errorf("search working memory: %w", err)
	}
	defer rows.Close()

	var out []WorkingMemoryMatch
	for rows.Next() {
		var m WorkingMemoryMatch
		var updatedStr string
		if err := rows.Scan(&m.ConversationID, &m.Content, &updatedStr, &m.Highlight); err != nil {
			return nil, fmt.Errorf("scan working memory match: %w", err)
		}
		if m.UpdatedAt, err = database.ParseTimestamp(updatedStr); err != nil {
			return nil, fmt.Errorf("parse working memory updated_at: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate working memory matches: %w", err)
	}
	return out, nil
}
