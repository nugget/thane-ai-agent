package memory

import (
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// MigrateUnifyMessages adds lifecycle columns to the working messages table and
// copies archived messages from archive.db into the unified table. This is the
// first step of the storage unification (issue #434).
//
// The migration is idempotent: it detects whether it has already run by checking
// for the status column and skips the archive merge if archive data is already
// present. Safe to call on every startup.
func MigrateUnifyMessages(workingDB *sql.DB, archiveDBPath string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	// Step 1: Add lifecycle columns if they don't exist.
	if err := addLifecycleColumns(workingDB, logger); err != nil {
		return fmt.Errorf("add lifecycle columns: %w", err)
	}

	// Step 2: Backfill status from compacted boolean.
	if err := backfillStatus(workingDB, logger); err != nil {
		return fmt.Errorf("backfill status: %w", err)
	}

	// Step 3: Merge archive data if archive.db exists and has data.
	if archiveDBPath != "" {
		if err := mergeArchiveMessages(workingDB, archiveDBPath, logger); err != nil {
			return fmt.Errorf("merge archive messages: %w", err)
		}
	}

	// Step 4: Ensure FTS index covers the unified table.
	if err := rebuildUnifiedFTS(workingDB, logger); err != nil {
		return fmt.Errorf("rebuild unified FTS: %w", err)
	}

	return nil
}

// addLifecycleColumns adds session_id, status, archived_at, archive_reason,
// and iteration_index to the messages table if they don't already exist.
func addLifecycleColumns(db *sql.DB, logger *slog.Logger) error {
	columns := []struct {
		name string
		sql  string
	}{
		{"session_id", "ALTER TABLE messages ADD COLUMN session_id TEXT"},
		{"status", "ALTER TABLE messages ADD COLUMN status TEXT DEFAULT 'active' CHECK (status IN ('active', 'compacted', 'archived'))"},
		{"archived_at", "ALTER TABLE messages ADD COLUMN archived_at TIMESTAMP"},
		{"archive_reason", "ALTER TABLE messages ADD COLUMN archive_reason TEXT"},
		{"iteration_index", "ALTER TABLE messages ADD COLUMN iteration_index INTEGER"},
	}

	for _, col := range columns {
		if hasColumn(db, "messages", col.name) {
			continue
		}
		if _, err := db.Exec(col.sql); err != nil {
			return fmt.Errorf("add column %s: %w", col.name, err)
		}
		logger.Info("added column to messages", "column", col.name)
	}

	// Add indexes for new columns.
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, timestamp)",
		"CREATE INDEX IF NOT EXISTS idx_messages_status ON messages(conversation_id, status)",
	}
	for _, idx := range indexes {
		if _, err := db.Exec(idx); err != nil {
			return fmt.Errorf("create index: %w", err)
		}
	}

	return nil
}

// backfillStatus sets the status column from the compacted boolean for
// existing rows that have status NULL (not yet migrated).
func backfillStatus(db *sql.DB, logger *slog.Logger) error {
	// Only backfill rows where status hasn't been set yet.
	result, err := db.Exec(`
		UPDATE messages SET status = 'compacted'
		WHERE compacted = TRUE AND (status IS NULL OR status = 'active')
	`)
	if err != nil {
		return fmt.Errorf("set compacted status: %w", err)
	}
	compacted, _ := result.RowsAffected()

	result, err = db.Exec(`
		UPDATE messages SET status = 'active'
		WHERE status IS NULL
	`)
	if err != nil {
		return fmt.Errorf("set active status: %w", err)
	}
	activated, _ := result.RowsAffected()

	if compacted > 0 || activated > 0 {
		logger.Info("backfilled message status",
			"compacted", compacted,
			"active", activated,
		)
	}

	return nil
}

// mergeArchiveMessages copies archived messages from archive.db into the
// unified messages table using UPSERT. For messages that already exist in
// the working DB, the archive's richer metadata (session_id, archived_at,
// archive_reason) is preserved via ON CONFLICT.
//
// SQLite supports UPSERT only with VALUES (not INSERT...SELECT), so we
// stage archive data in a temp table, read rows in Go, and UPSERT each
// row with a prepared statement in a transaction.
func mergeArchiveMessages(workingDB *sql.DB, archiveDBPath string, logger *slog.Logger) error {
	// Check if we already have archived messages (migration already ran).
	var archivedCount int
	_ = workingDB.QueryRow(`SELECT COUNT(*) FROM messages WHERE status = 'archived'`).Scan(&archivedCount)
	if archivedCount > 0 {
		logger.Debug("archive messages already merged", "count", archivedCount)
		return nil
	}

	// Check if archive DB exists by trying to attach it.
	_, err := workingDB.Exec(`ATTACH DATABASE ? AS archive`, archiveDBPath)
	if err != nil {
		logger.Info("no archive database to merge", "path", archiveDBPath, "error", err)
		return nil
	}

	// Check if archive has the archive_messages table.
	var tableExists int
	err = workingDB.QueryRow(`SELECT COUNT(*) FROM archive.sqlite_master WHERE type='table' AND name='archive_messages'`).Scan(&tableExists)
	if err != nil || tableExists == 0 {
		_, _ = workingDB.Exec(`DETACH DATABASE archive`)
		logger.Info("archive database has no archive_messages table")
		return nil
	}

	// Count source rows for logging.
	var sourceCount int
	_ = workingDB.QueryRow(`SELECT COUNT(*) FROM archive.archive_messages`).Scan(&sourceCount)
	if sourceCount == 0 {
		_, _ = workingDB.Exec(`DETACH DATABASE archive`)
		logger.Info("archive database has no messages to merge")
		return nil
	}

	logger.Info("merging archive messages into unified table", "source_count", sourceCount)
	start := time.Now()

	// Ensure the conversations referenced by archive messages exist in the
	// working DB (they may not if the working DB was cleared).
	_, err = workingDB.Exec(`
		INSERT OR IGNORE INTO conversations (id, created_at, updated_at)
		SELECT DISTINCT conversation_id, MIN(timestamp), MAX(timestamp)
		FROM archive.archive_messages
		GROUP BY conversation_id
	`)
	if err != nil {
		_, _ = workingDB.Exec(`DETACH DATABASE archive`)
		return fmt.Errorf("ensure conversations: %w", err)
	}

	// Stage archive data into a temp table so we can detach the archive DB
	// before the UPSERT loop.
	_, err = workingDB.Exec(`
		CREATE TEMP TABLE _archive_import AS
		SELECT id, conversation_id, session_id, role, content, timestamp,
		       token_count, tool_calls, tool_call_id, archived_at, archive_reason
		FROM archive.archive_messages
	`)
	if err != nil {
		_, _ = workingDB.Exec(`DETACH DATABASE archive`)
		return fmt.Errorf("stage archive data: %w", err)
	}
	_, _ = workingDB.Exec(`DETACH DATABASE archive`)

	// Read staged rows and UPSERT each one. SQLite only supports ON CONFLICT
	// with VALUES (not INSERT...SELECT), so we iterate in Go with a prepared
	// statement inside a transaction. ~115K rows takes <1s in WAL mode.
	affected, err := upsertFromTemp(workingDB)
	if err != nil {
		_, _ = workingDB.Exec(`DROP TABLE IF EXISTS _archive_import`)
		return fmt.Errorf("upsert archive messages: %w", err)
	}

	_, _ = workingDB.Exec(`DROP TABLE IF EXISTS _archive_import`)

	logger.Info("archive messages merged",
		"rows_affected", affected,
		"duration", time.Since(start).Round(time.Millisecond),
	)

	return nil
}

// upsertFromTemp reads rows from _archive_import and UPSERTs each into the
// messages table within a transaction. Returns the number of rows processed.
func upsertFromTemp(db *sql.DB) (int64, error) {
	rows, err := db.Query(`
		SELECT id, conversation_id, session_id, role, content, timestamp,
		       token_count, tool_calls, tool_call_id, archived_at, archive_reason
		FROM _archive_import
	`)
	if err != nil {
		return 0, fmt.Errorf("read staged data: %w", err)
	}
	defer rows.Close()

	// Collect rows before starting the transaction (SQLite can't have
	// concurrent readers and writers on the same connection).
	type archiveRow struct {
		id, convID, sessID, role, content, ts string
		tokenCount                            int
		toolCalls, toolCallID                 sql.NullString
		archivedAt, archiveReason             sql.NullString
	}
	var staged []archiveRow
	for rows.Next() {
		var r archiveRow
		if err := rows.Scan(&r.id, &r.convID, &r.sessID, &r.role, &r.content,
			&r.ts, &r.tokenCount, &r.toolCalls, &r.toolCallID,
			&r.archivedAt, &r.archiveReason); err != nil {
			return 0, fmt.Errorf("scan staged row: %w", err)
		}
		staged = append(staged, r)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate staged rows: %w", err)
	}
	rows.Close()

	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		INSERT INTO messages
			(id, conversation_id, session_id, role, content, timestamp,
			 token_count, tool_calls, tool_call_id,
			 compacted, status, archived_at, archive_reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, FALSE, 'archived', ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_id = COALESCE(excluded.session_id, messages.session_id),
			status = 'archived',
			archived_at = COALESCE(excluded.archived_at, messages.archived_at),
			archive_reason = COALESCE(excluded.archive_reason, messages.archive_reason)
	`)
	if err != nil {
		return 0, fmt.Errorf("prepare upsert: %w", err)
	}
	defer stmt.Close()

	for _, r := range staged {
		if _, err := stmt.Exec(r.id, r.convID, r.sessID, r.role, r.content,
			r.ts, r.tokenCount, r.toolCalls, r.toolCallID,
			r.archivedAt, r.archiveReason); err != nil {
			return 0, fmt.Errorf("upsert row %s: %w", r.id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	return int64(len(staged)), nil
}

// rebuildUnifiedFTS creates or rebuilds the FTS5 index on the unified
// messages table.
func rebuildUnifiedFTS(db *sql.DB, logger *slog.Logger) error {
	// Try creating the FTS table. If FTS5 isn't available, skip silently.
	_, err := db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
			content,
			content=messages,
			content_rowid=rowid
		)
	`)
	if err != nil {
		logger.Debug("FTS5 not available for unified messages", "error", err)
		return nil
	}

	// Rebuild the index to pick up all messages.
	_, err = db.Exec(`INSERT INTO messages_fts(messages_fts) VALUES('rebuild')`)
	if err != nil {
		return fmt.Errorf("rebuild messages_fts: %w", err)
	}

	logger.Info("unified messages FTS index rebuilt")
	return nil
}

// hasColumn checks whether a column exists on the given table.
// Both table and column must be valid SQL identifiers (alphanumeric + underscore).
func hasColumn(db *sql.DB, table, column string) bool {
	if !isValidIdentifier(table) || !isValidIdentifier(column) {
		return false
	}
	// Use a lightweight SELECT to probe for the column.
	_, err := db.Exec("SELECT " + column + " FROM " + table + " LIMIT 0")
	return err == nil
}

// isValidIdentifier returns true if s is a safe SQL identifier
// (non-empty, only ASCII letters, digits, and underscores).
func isValidIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		isAlpha := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !isAlpha && c != '_' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
