package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// SearchBundle is the multi-surface result envelope. Always carries
// all three slices — an empty slice means "no hits on this surface"
// (the non-nil zero-length list is the explicit signal to the
// model). Truncated propagates from the underlying raw-message
// search; distilled surfaces don't truncate within a single call.
type SearchBundle struct {
	Messages      []SearchResult
	Sessions      []SessionMatch
	WorkingMemory []WorkingMemoryMatch
	Truncated     bool
}

// MemorySearcher is the unified read-side interface across the
// memory surfaces. Both the model-initiated archive_search tool and
// the wake-time prewarm provider consume this same interface, so
// the two paths cannot drift — any retrieval improvement applies
// uniformly to both. Implementations: [MemorySearch] for production
// (composed from the real stores), and test mocks.
//
// **Contract:** Search MUST return a non-nil *SearchBundle whenever
// it returns a nil error. Callers may rely on this — "no hits" is
// signaled by an empty bundle (all three slices empty), not by a nil
// bundle. A nil bundle paired with a nil error is treated as a
// programming bug by the prewarm provider and logged as a soft
// fault. Returning a non-nil error means the bundle value is
// undefined and callers should not read it.
type MemorySearcher interface {
	Search(opts SearchOptions) (*SearchBundle, error)
}

// MemorySearch is the production MemorySearcher: composes
// [ArchiveStore] (raw messages via messages_fts, session summaries
// via sessions_fts) and [WorkingMemoryStore] (per-conversation
// distilled state via working_memory_fts) into a single search
// surface.
//
// Session and working_memory hits soft-fail individually — a query
// error on a distilled surface doesn't suppress the raw-message
// results the model would otherwise have seen. Only a raw-message
// search failure propagates.
type MemorySearch struct {
	archive *ArchiveStore
	working *WorkingMemoryStore
	logger  *slog.Logger
}

// distilledSurfaceLimit caps how many session and working_memory
// matches each query returns. Distilled hits are dense per byte
// compared to raw messages with context windows, so a small cap
// keeps the model-visible envelope readable without sacrificing
// useful coverage. Working memory in particular is one row per
// conversation — more than 3 hits rarely tells the model anything
// new at this stage of retrieval.
const (
	maxDistilledSessions      = 5
	maxDistilledWorkingMemory = 3
)

// NewMemorySearch builds a MemorySearch from the underlying stores.
// Either store may be nil at construction time (e.g. during early
// startup or in tests that only exercise one surface); a nil archive
// makes Search return an empty bundle, a nil working memory store
// just skips the working_memory surface.
func NewMemorySearch(archive *ArchiveStore, working *WorkingMemoryStore, logger *slog.Logger) *MemorySearch {
	if logger == nil {
		logger = slog.Default()
	}
	return &MemorySearch{archive: archive, working: working, logger: logger}
}

// Search runs the query across every available memory surface and
// returns a SearchBundle. The raw-message path uses opts.Limit; the
// distilled surfaces use their own tighter caps so the envelope
// stays bounded.
func (m *MemorySearch) Search(opts SearchOptions) (*SearchBundle, error) {
	bundle := &SearchBundle{}
	if m.archive == nil {
		return bundle, nil
	}

	msgs, err := m.archive.Search(opts)
	if err != nil {
		return nil, err
	}
	bundle.Messages = msgs

	// Session summaries. Soft-fail: a sessions_fts query error
	// shouldn't drop the raw-message results we already collected.
	if sess, err := m.archive.SearchSessions(opts.Query, maxDistilledSessions); err == nil {
		bundle.Sessions = sess
	} else if m.logger != nil {
		m.logger.Warn("session summaries search failed", "query", opts.Query, "error", err)
	}

	// Working memory. Soft-fail for the same reason.
	if m.working != nil {
		if wm, err := m.working.Search(opts.Query, maxDistilledWorkingMemory); err == nil {
			bundle.WorkingMemory = wm
		} else if m.logger != nil {
			m.logger.Warn("working memory search failed", "query", opts.Query, "error", err)
		}
	}

	return bundle, nil
}

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
	//    index is incomplete — i.e., the _docsize shadow table holds
	//    fewer rows than the source `sessions` table.
	//
	//    `_docsize == 0` is not a sufficient trigger by itself: if a
	//    previous startup's rebuild failed AFTER the table + triggers
	//    were created, the AI trigger would index every new session
	//    going forward, pushing _docsize above 0 — but the historical
	//    pre-trigger rows would stay invisible forever. Comparing
	//    _docsize against COUNT(sessions) catches this case on the
	//    next startup and re-runs the rebuild idempotently.
	//
	//    Using the shadow table rather than COUNT(*) on sessions_fts:
	//    external-content FTS5 proxies SELECT COUNT(*) through to the
	//    source `sessions` table, which would always self-equal and
	//    suppress the rebuild. Only the shadow tables (_docsize,
	//    _data, _idx) reflect the inverted index's actual contents.
	var docCount, sessCount int
	if err := db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s_docsize`, sessionsFTSTable)).Scan(&docCount); err != nil {
		if s.logger != nil {
			s.logger.Warn("sessions_fts docsize probe failed", "error", err)
		}
		return true // triggers are installed — degrade gracefully
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessCount); err != nil {
		if s.logger != nil {
			s.logger.Warn("sessions count probe failed", "error", err)
		}
		return true
	}
	if docCount < sessCount {
		if _, err := db.Exec(fmt.Sprintf(`INSERT INTO %s(%s) VALUES('rebuild')`, sessionsFTSTable, sessionsFTSTable)); err != nil {
			if s.logger != nil {
				s.logger.Warn("sessions_fts backfill failed; next startup will retry",
					"docsize", docCount,
					"sessions", sessCount,
					"error", err)
			}
			// Setup ends in "table + triggers installed, index partial."
			// Returning true is correct: new writes via triggers will
			// keep the index growing while we wait for the next startup
			// to retry the rebuild against the same shortfall signal.
		}
	}

	return true
}

// SessionMatch is the per-row shape returned from [ArchiveStore.SearchSessions].
// Each match carries the session's identifying metadata plus the
// snippet highlight FTS5 produced from whichever indexed column
// matched. Use SessionID with the existing archive_session_transcript
// retrieval to pull the full conversation that generated the summary.
//
// Tags is unmarshaled from the JSON blob stored in sessions.tags so
// callers get a consistent [][]string-shaped tag list — matching
// [Session.Tags] and freeing callers from having to parse JSON
// themselves.
type SessionMatch struct {
	SessionID      string    `json:"session_id"`
	ConversationID string    `json:"conversation_id"`
	StartedAt      time.Time `json:"started_at"`
	EndedAt        time.Time `json:"ended_at,omitempty"`
	Title          string    `json:"title"`
	Summary        string    `json:"summary"`
	Tags           []string  `json:"tags,omitempty"`
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
	// Gate on the sessions-specific FTS setup, not the core ftsEnabled
	// flag. trySetupSessionsFTS can fail independently (corrupt
	// pre-existing virtual table, shadow-table permissions issue) even
	// when messages_fts is healthy — gating here ensures we degrade to
	// "no hits" instead of erroring against a missing/broken FTS table.
	if !s.sessionsFTSEnabled {
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
		       COALESCE(s.tags, '') AS tags_json,
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
		var startedStr, endedStr, tagsJSON string
		if err := rows.Scan(
			&m.SessionID, &m.ConversationID, &startedStr, &endedStr,
			&m.Title, &m.Summary, &tagsJSON, &m.Highlight,
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
		// Tags is stored as a JSON blob ([]string) — unmarshal so the
		// caller gets a consistent shape with Session.Tags. Corrupt JSON
		// is logged but not fatal (matches populateSession's posture).
		if tagsJSON != "" {
			if err := json.Unmarshal([]byte(tagsJSON), &m.Tags); err != nil {
				if s.logger != nil {
					s.logger.Warn("corrupt tags JSON on session match",
						"session", ShortID(m.SessionID), "error", err)
				}
				m.Tags = nil
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
