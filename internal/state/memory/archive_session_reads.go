package memory

import (
	"context"
	"fmt"
)

// sessionMessageCountQuery counts one session's archived messages. It is
// served by the session index on messages, so it costs the same for a
// session with one message as for a session with ten thousand.
const sessionMessageCountQuery = `SELECT COUNT(*) FROM messages WHERE session_id = ?`

// GetSession retrieves a session by ID. Callers with a request context
// should use [ArchiveStore.GetSessionContext].
func (s *ArchiveStore) GetSession(sessionID string) (*Session, error) {
	return s.GetSessionContext(context.Background(), sessionID)
}

// GetSessionContext retrieves a session by ID under ctx, so a cancelled
// or expired caller stops paying for the lookup and the message count
// that goes with it. A session that does not exist is a nil session and
// a nil error.
func (s *ArchiveStore) GetSessionContext(ctx context.Context, sessionID string) (*Session, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, conversation_id, started_at, ended_at, end_reason,
		       0 AS message_count,
		       summary, title, tags, metadata, parent_session_id, parent_tool_call_id
		FROM sessions WHERE id = ?
	`, sessionID)

	sess, err := s.scanSession(row)
	if sess != nil {
		sess.MessageCount = s.countSessionMessages(ctx, sess.ID)
	}
	return sess, err
}

// countSessionMessages populates MessageCount on individual session
// lookups. A failed count — including a cancelled one — leaves the count
// at zero rather than failing the lookup, because the session itself has
// already been read and is what the caller asked for.
func (s *ArchiveStore) countSessionMessages(ctx context.Context, sessionID string) int {
	var count int
	_ = s.db.QueryRowContext(ctx, sessionMessageCountQuery, sessionID).Scan(&count)
	return count
}

// GetSessionTranscript returns all archived messages for a session in
// chronological order. Callers with a request context should use
// [ArchiveStore.GetSessionTranscriptContext].
func (s *ArchiveStore) GetSessionTranscript(sessionID string) ([]Message, error) {
	return s.GetSessionTranscriptContext(context.Background(), sessionID)
}

// GetSessionTranscriptContext is [ArchiveStore.GetSessionTranscript]
// under ctx. This is the read the cancellation is worth the most on: it
// scans, decodes and returns every message a session ever held, so a
// caller past its deadline stops the one archive read whose cost grows
// with the session rather than only the indexed lookups around it.
func (s *ArchiveStore) GetSessionTranscriptContext(ctx context.Context, sessionID string) ([]Message, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM messages
		WHERE session_id = ?
		ORDER BY timestamp ASC
	`, s.msgSelectCols())

	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get transcript: %w", err)
	}
	defer rows.Close()

	return s.scanMessages(rows)
}
