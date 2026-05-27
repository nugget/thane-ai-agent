package memory

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// sessionsFTSTable is the FTS5 virtual table name covering the
// sessions table's distilled columns. Indexes title, summary, and
// tags so a single query against a household-vocabulary phrase can
// reach the summarizer's per-session output. Held alongside the
// raw-message index [ArchiveStore.msgFTSName].
const sessionsFTSTable = "sessions_fts"

// workingMemoryFTSTable is the FTS5 virtual table name covering
// working_memory.content. Working memory is per-conversation living
// distillation written by the metacog loop — small but high
// signal-density.
const workingMemoryFTSTable = "working_memory_fts"

// trySetupSessionsFTS creates the sessions_fts virtual table, the
// AI/AD/AU sync triggers, and backfills any rows that exist in
// sessions but not yet in sessions_fts. Returns true on success.
// Idempotent — re-running against an initialized store is a no-op
// beyond the existence checks.
//
// Mirrors the shape SQLite expects for external-content FTS5: rows
// only appear in sessions_fts when an INSERT/UPDATE on sessions
// fires one of the triggers, or when the backfill runs once at
// startup against a fresh index.
func (s *ArchiveStore) trySetupSessionsFTS() bool {
	if !s.ftsEnabled {
		return false
	}
	db := s.db
	if db == nil {
		return false
	}

	// 1. Virtual table. External-content over the sessions table by
	//    rowid. The column list is what BM25 ranks over.
	stmts := []string{
		fmt.Sprintf(`
			CREATE VIRTUAL TABLE IF NOT EXISTS %s USING fts5(
				title, summary, tags,
				content=sessions, content_rowid=rowid
			)
		`, sessionsFTSTable),

		// 2. Sync triggers. AI on insert, AD on delete, AU on update —
		//    the AU pattern is "delete then insert" because FTS5 can't
		//    do partial-column updates on external-content tables.
		fmt.Sprintf(`
			CREATE TRIGGER IF NOT EXISTS sessions_fts_ai AFTER INSERT ON sessions BEGIN
				INSERT INTO %s(rowid, title, summary, tags)
				VALUES (new.rowid,
				        COALESCE(new.title, ''),
				        COALESCE(new.summary, ''),
				        COALESCE(new.tags, ''));
			END
		`, sessionsFTSTable),
		fmt.Sprintf(`
			CREATE TRIGGER IF NOT EXISTS sessions_fts_ad AFTER DELETE ON sessions BEGIN
				INSERT INTO %s(%s, rowid, title, summary, tags)
				VALUES ('delete', old.rowid,
				        COALESCE(old.title, ''),
				        COALESCE(old.summary, ''),
				        COALESCE(old.tags, ''));
			END
		`, sessionsFTSTable, sessionsFTSTable),
		fmt.Sprintf(`
			CREATE TRIGGER IF NOT EXISTS sessions_fts_au AFTER UPDATE ON sessions BEGIN
				INSERT INTO %s(%s, rowid, title, summary, tags)
				VALUES ('delete', old.rowid,
				        COALESCE(old.title, ''),
				        COALESCE(old.summary, ''),
				        COALESCE(old.tags, ''));
				INSERT INTO %s(rowid, title, summary, tags)
				VALUES (new.rowid,
				        COALESCE(new.title, ''),
				        COALESCE(new.summary, ''),
				        COALESCE(new.tags, ''));
			END
		`, sessionsFTSTable, sessionsFTSTable, sessionsFTSTable),
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			if s.logger != nil {
				s.logger.Warn("sessions_fts setup failed", "error", err)
			}
			return false
		}
	}

	// 3. Backfill via FTS5's 'rebuild' command when the inverted
	//    index is empty. Runs only on first init: once any session
	//    has been indexed (either by this backfill or by AI/AU
	//    triggers as the summarizer produces metadata), steady-state
	//    sync is the triggers' job.
	//
	//    Empty-check probes the _docsize shadow table, not COUNT(*)
	//    on the virtual table — external-content FTS5 proxies
	//    SELECT COUNT(*) through to the source `sessions` table and
	//    so always reports > 0 whenever any session exists. Only the
	//    shadow tables (_docsize, _data, _idx) tell you whether the
	//    inverted index itself has any tokenized rows.
	var docCount int
	if err := db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s_docsize`, sessionsFTSTable)).Scan(&docCount); err != nil {
		if s.logger != nil {
			s.logger.Warn("sessions_fts docsize probe failed", "error", err)
		}
		return true // triggers are installed — degrade gracefully
	}
	if docCount == 0 {
		if _, err := db.Exec(fmt.Sprintf(`INSERT INTO %s(%s) VALUES('rebuild')`, sessionsFTSTable, sessionsFTSTable)); err != nil {
			if s.logger != nil {
				s.logger.Warn("sessions_fts backfill failed", "error", err)
			}
			// Don't fail the whole setup — the AI trigger handles new
			// sessions; existing rows can be backfilled by the next
			// startup retrying.
		}
	}

	return true
}

// SessionMatch is the per-row shape returned from [ArchiveStore.SearchSessions].
// Each match carries the session's identifying metadata plus the
// snippet highlight FTS5 produced from whichever indexed column
// matched. Use SessionID with the existing archive_session_transcript
// retrieval to pull the full conversation that generated the summary.
type SessionMatch struct {
	SessionID      string    `json:"session_id"`
	ConversationID string    `json:"conversation_id"`
	StartedAt      time.Time `json:"started_at"`
	EndedAt        time.Time `json:"ended_at,omitempty"`
	Title          string    `json:"title"`
	Summary        string    `json:"summary"`
	Tags           string    `json:"tags,omitempty"`
	Highlight      string    `json:"highlight"`
}

// SearchSessions runs an FTS5 query against sessions_fts and returns
// the highest-ranking session summaries by BM25. The query is wrapped
// as a phrase token via [phraseFTS5Query] for the same reason the
// raw-message search prefers phrase-anchored hits: distilled summary
// text rewards literal phrase matches over bag-of-OR-terms recall.
//
// Returns empty slice (not error) when FTS5 isn't available, when
// the query trims to empty, or when no rows match. Caller composes
// this with [ArchiveStore.Search] to build a multi-surface envelope.
func (s *ArchiveStore) SearchSessions(query string, limit int) ([]SessionMatch, error) {
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

	sqlText := fmt.Sprintf(`
		SELECT s.id, s.conversation_id, s.started_at,
		       COALESCE(s.ended_at, '') AS ended_at,
		       COALESCE(s.title, ''),
		       COALESCE(s.summary, ''),
		       COALESCE(s.tags, ''),
		       snippet(%s, -1, '**', '**', '...', 32) AS highlight
		FROM %s
		JOIN sessions s ON s.rowid = %s.rowid
		WHERE %s MATCH ?
		ORDER BY rank
		LIMIT ?
	`, sessionsFTSTable, sessionsFTSTable, sessionsFTSTable, sessionsFTSTable)

	rows, err := s.db.Query(sqlText, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search sessions: %w", err)
	}
	defer rows.Close()

	var out []SessionMatch
	for rows.Next() {
		var m SessionMatch
		var startedStr, endedStr string
		if err := rows.Scan(
			&m.SessionID, &m.ConversationID, &startedStr, &endedStr,
			&m.Title, &m.Summary, &m.Tags, &m.Highlight,
		); err != nil {
			return nil, fmt.Errorf("scan session match: %w", err)
		}
		if m.StartedAt, err = database.ParseTimestamp(startedStr); err != nil {
			return nil, fmt.Errorf("parse session started_at: %w", err)
		}
		if endedStr != "" {
			if m.EndedAt, err = database.ParseTimestamp(endedStr); err != nil {
				return nil, fmt.Errorf("parse session ended_at: %w", err)
			}
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session matches: %w", err)
	}
	return out, nil
}

// trySetupWorkingMemoryFTS creates working_memory_fts, its sync
// triggers, and backfills existing rows. Same shape as
// [ArchiveStore.trySetupSessionsFTS] but for the per-conversation
// living-distillation table.
//
// Called from [WorkingMemoryStore]'s migration path; takes the shared
// FTS5-availability gate as an argument so the working-memory store
// doesn't need to re-probe FTS5 separately.
func trySetupWorkingMemoryFTS(db *sql.DB, ftsEnabled bool) bool {
	if !ftsEnabled || db == nil {
		return false
	}
	stmts := []string{
		fmt.Sprintf(`
			CREATE VIRTUAL TABLE IF NOT EXISTS %s USING fts5(
				content,
				content=working_memory, content_rowid=rowid
			)
		`, workingMemoryFTSTable),
		fmt.Sprintf(`
			CREATE TRIGGER IF NOT EXISTS working_memory_fts_ai AFTER INSERT ON working_memory BEGIN
				INSERT INTO %s(rowid, content) VALUES (new.rowid, new.content);
			END
		`, workingMemoryFTSTable),
		fmt.Sprintf(`
			CREATE TRIGGER IF NOT EXISTS working_memory_fts_ad AFTER DELETE ON working_memory BEGIN
				INSERT INTO %s(%s, rowid, content) VALUES ('delete', old.rowid, old.content);
			END
		`, workingMemoryFTSTable, workingMemoryFTSTable),
		fmt.Sprintf(`
			CREATE TRIGGER IF NOT EXISTS working_memory_fts_au AFTER UPDATE ON working_memory BEGIN
				INSERT INTO %s(%s, rowid, content) VALUES ('delete', old.rowid, old.content);
				INSERT INTO %s(rowid, content) VALUES (new.rowid, new.content);
			END
		`, workingMemoryFTSTable, workingMemoryFTSTable, workingMemoryFTSTable),
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return false
		}
	}
	// Backfill via 'rebuild' when the inverted index is empty. Same
	// shadow-table semantics as [ArchiveStore.trySetupSessionsFTS] —
	// COUNT(*) on the virtual table proxies through to the source,
	// so only `_docsize` is honest about whether the index has
	// tokenized rows.
	var docCount int
	if err := db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s_docsize`, workingMemoryFTSTable)).Scan(&docCount); err != nil {
		return true
	}
	if docCount == 0 {
		_, _ = db.Exec(fmt.Sprintf(`INSERT INTO %s(%s) VALUES('rebuild')`, workingMemoryFTSTable, workingMemoryFTSTable))
	}
	return true
}

// WorkingMemoryMatch is the per-row shape returned from
// [WorkingMemoryStore.Search]. Working memory is keyed by
// conversation_id, so the match identifies which conversation's
// distillation matched and when it was last updated; the caller
// can follow up with [WorkingMemoryStore.Get] to pull the full
// content if the snippet looks promising.
type WorkingMemoryMatch struct {
	ConversationID string    `json:"conversation_id"`
	UpdatedAt      time.Time `json:"updated_at"`
	Content        string    `json:"content"`
	Highlight      string    `json:"highlight"`
}
