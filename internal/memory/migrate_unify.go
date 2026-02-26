package memory

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
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
// messages table and installs triggers to keep it in sync.
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

	// Install triggers to keep messages_fts in sync with the messages table.
	// External-content FTS5 tables require explicit sync via these triggers;
	// without them, rows inserted after the initial rebuild won't be searchable.
	triggers := []string{
		`CREATE TRIGGER IF NOT EXISTS messages_fts_ai AFTER INSERT ON messages BEGIN
			INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS messages_fts_ad AFTER DELETE ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, content) VALUES('delete', old.rowid, old.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS messages_fts_au AFTER UPDATE ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, content) VALUES('delete', old.rowid, old.content);
			INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
		END`,
	}
	for _, trigger := range triggers {
		if _, err := db.Exec(trigger); err != nil {
			return fmt.Errorf("create FTS trigger: %w", err)
		}
	}

	// Rebuild the index to pick up all existing messages.
	_, err = db.Exec(`INSERT INTO messages_fts(messages_fts) VALUES('rebuild')`)
	if err != nil {
		return fmt.Errorf("rebuild messages_fts: %w", err)
	}

	logger.Info("unified messages FTS index rebuilt")
	return nil
}

// MigrateUnifyToolCalls adds lifecycle columns to the working tool_calls table
// and copies archived tool calls from archive.db into the unified table. This
// is the second step of the storage unification (issue #434).
//
// The migration is idempotent: it detects whether it has already run by
// checking for the status column and skips the archive merge if archive data
// is already present. Safe to call on every startup.
func MigrateUnifyToolCalls(workingDB *sql.DB, archiveDBPath string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	// Step 1: Add lifecycle columns if they don't exist.
	if err := addToolCallLifecycleColumns(workingDB, logger); err != nil {
		return fmt.Errorf("add tool call lifecycle columns: %w", err)
	}

	// Step 2: Backfill status for existing rows.
	if err := backfillToolCallStatus(workingDB, logger); err != nil {
		return fmt.Errorf("backfill tool call status: %w", err)
	}

	// Step 3: Merge archive data if archive.db exists and has data.
	if archiveDBPath != "" {
		if err := mergeArchiveToolCalls(workingDB, archiveDBPath, logger); err != nil {
			return fmt.Errorf("merge archive tool calls: %w", err)
		}
	}

	return nil
}

// addToolCallLifecycleColumns adds session_id, status, archived_at, and
// iteration_index to the tool_calls table if they don't already exist.
func addToolCallLifecycleColumns(db *sql.DB, logger *slog.Logger) error {
	columns := []struct {
		name string
		sql  string
	}{
		{"session_id", "ALTER TABLE tool_calls ADD COLUMN session_id TEXT"},
		{"status", "ALTER TABLE tool_calls ADD COLUMN status TEXT DEFAULT 'active' CHECK (status IN ('active', 'archived'))"},
		{"archived_at", "ALTER TABLE tool_calls ADD COLUMN archived_at TIMESTAMP"},
		{"iteration_index", "ALTER TABLE tool_calls ADD COLUMN iteration_index INTEGER"},
	}

	for _, col := range columns {
		if hasColumn(db, "tool_calls", col.name) {
			continue
		}
		if _, err := db.Exec(col.sql); err != nil {
			return fmt.Errorf("add column %s: %w", col.name, err)
		}
		logger.Info("added column to tool_calls", "column", col.name)
	}

	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_tool_calls_session ON tool_calls(session_id, started_at)",
		"CREATE INDEX IF NOT EXISTS idx_tool_calls_status ON tool_calls(conversation_id, status)",
	}
	for _, idx := range indexes {
		if _, err := db.Exec(idx); err != nil {
			return fmt.Errorf("create index: %w", err)
		}
	}

	return nil
}

// backfillToolCallStatus sets status = 'active' on any tool_calls rows where
// status is NULL (pre-migration rows). Tool calls have no compacted state.
func backfillToolCallStatus(db *sql.DB, logger *slog.Logger) error {
	result, err := db.Exec(`UPDATE tool_calls SET status = 'active' WHERE status IS NULL`)
	if err != nil {
		return fmt.Errorf("set active status: %w", err)
	}
	activated, _ := result.RowsAffected()
	if activated > 0 {
		logger.Info("backfilled tool call status", "active", activated)
	}
	return nil
}

// mergeArchiveToolCalls copies archived tool calls from archive.db into the
// unified tool_calls table using UPSERT. For tool calls that already exist in
// the working DB, the archive's metadata (session_id, archived_at,
// iteration_index) is preserved via ON CONFLICT.
func mergeArchiveToolCalls(workingDB *sql.DB, archiveDBPath string, logger *slog.Logger) error {
	// Check if we already have archived tool calls (migration already ran).
	var archivedCount int
	_ = workingDB.QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE status = 'archived'`).Scan(&archivedCount)
	if archivedCount > 0 {
		logger.Debug("archive tool calls already merged", "count", archivedCount)
		return nil
	}

	// Check if archive DB exists by trying to attach it.
	_, err := workingDB.Exec(`ATTACH DATABASE ? AS archive`, archiveDBPath)
	if err != nil {
		logger.Info("no archive database to merge tool calls", "path", archiveDBPath, "error", err)
		return nil
	}

	// Check if archive has the archive_tool_calls table.
	var tableExists int
	err = workingDB.QueryRow(`SELECT COUNT(*) FROM archive.sqlite_master WHERE type='table' AND name='archive_tool_calls'`).Scan(&tableExists)
	if err != nil || tableExists == 0 {
		_, _ = workingDB.Exec(`DETACH DATABASE archive`)
		logger.Info("archive database has no archive_tool_calls table")
		return nil
	}

	// Count source rows for logging.
	var sourceCount int
	_ = workingDB.QueryRow(`SELECT COUNT(*) FROM archive.archive_tool_calls`).Scan(&sourceCount)
	if sourceCount == 0 {
		_, _ = workingDB.Exec(`DETACH DATABASE archive`)
		logger.Info("archive database has no tool calls to merge")
		return nil
	}

	logger.Info("merging archive tool calls into unified table", "source_count", sourceCount)
	start := time.Now()

	// Ensure the conversations referenced by archive tool calls exist in the
	// working DB (they may not if the working DB was cleared).
	_, err = workingDB.Exec(`
		INSERT OR IGNORE INTO conversations (id, created_at, updated_at)
		SELECT DISTINCT conversation_id, MIN(started_at), MAX(started_at)
		FROM archive.archive_tool_calls
		GROUP BY conversation_id
	`)
	if err != nil {
		_, _ = workingDB.Exec(`DETACH DATABASE archive`)
		return fmt.Errorf("ensure conversations: %w", err)
	}

	// Stage archive data into a temp table so we can detach the archive DB
	// before the UPSERT loop.
	_, err = workingDB.Exec(`
		CREATE TEMP TABLE _archive_tc_import AS
		SELECT id, conversation_id, session_id, tool_name, arguments,
		       result, error, started_at, completed_at, duration_ms,
		       archived_at, iteration_index
		FROM archive.archive_tool_calls
	`)
	if err != nil {
		_, _ = workingDB.Exec(`DETACH DATABASE archive`)
		return fmt.Errorf("stage archive tool call data: %w", err)
	}
	_, _ = workingDB.Exec(`DETACH DATABASE archive`)

	// Read staged rows and UPSERT each one.
	affected, err := upsertToolCallsFromTemp(workingDB)
	if err != nil {
		_, _ = workingDB.Exec(`DROP TABLE IF EXISTS _archive_tc_import`)
		return fmt.Errorf("upsert archive tool calls: %w", err)
	}

	_, _ = workingDB.Exec(`DROP TABLE IF EXISTS _archive_tc_import`)

	logger.Info("archive tool calls merged",
		"rows_affected", affected,
		"duration", time.Since(start).Round(time.Millisecond),
	)

	return nil
}

// upsertToolCallsFromTemp reads rows from _archive_tc_import and UPSERTs
// each into the tool_calls table within a transaction.
func upsertToolCallsFromTemp(db *sql.DB) (int64, error) {
	rows, err := db.Query(`
		SELECT id, conversation_id, session_id, tool_name, arguments,
		       result, error, started_at, completed_at, duration_ms,
		       archived_at, iteration_index
		FROM _archive_tc_import
	`)
	if err != nil {
		return 0, fmt.Errorf("read staged data: %w", err)
	}
	defer rows.Close()

	type tcRow struct {
		id, convID, sessID, toolName, arguments string
		result, errMsg                          sql.NullString
		startedAt                               string
		completedAt                             sql.NullString
		durationMs                              sql.NullInt64
		archivedAt                              sql.NullString
		iterationIndex                          sql.NullInt64
	}
	var staged []tcRow
	for rows.Next() {
		var r tcRow
		if err := rows.Scan(&r.id, &r.convID, &r.sessID, &r.toolName, &r.arguments,
			&r.result, &r.errMsg, &r.startedAt, &r.completedAt, &r.durationMs,
			&r.archivedAt, &r.iterationIndex); err != nil {
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
		INSERT INTO tool_calls
			(id, message_id, conversation_id, session_id, tool_name, arguments,
			 result, error, started_at, completed_at, duration_ms,
			 status, archived_at, iteration_index)
		VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'archived', ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_id = COALESCE(excluded.session_id, tool_calls.session_id),
			status = 'archived',
			archived_at = COALESCE(excluded.archived_at, tool_calls.archived_at),
			iteration_index = COALESCE(excluded.iteration_index, tool_calls.iteration_index)
	`)
	if err != nil {
		return 0, fmt.Errorf("prepare upsert: %w", err)
	}
	defer stmt.Close()

	for _, r := range staged {
		if _, err := stmt.Exec(r.id, r.convID, r.sessID, r.toolName, r.arguments,
			r.result, r.errMsg, r.startedAt, r.completedAt, r.durationMs,
			r.archivedAt, r.iterationIndex); err != nil {
			return 0, fmt.Errorf("upsert row %s: %w", r.id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	return int64(len(staged)), nil
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

// MigrateConsolidateDB copies session-related tables from archive.db into the
// working database (thane.db) so all data lives in a single file. This is the
// third step of the storage unification (issue #434): after messages and tool
// calls were unified in phases 1+2, this phase moves sessions,
// archive_iterations, import_metadata, working_memory, and delegations.
//
// The migration is idempotent: it checks for existing rows in the sessions
// table as a sentinel. If archive.db does not exist, it returns nil (fresh
// install). Safe to call on every startup.
func MigrateConsolidateDB(workingDB *sql.DB, archiveDBPath string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	// Step 1: Create target tables in workingDB (idempotent).
	if err := createConsolidationTargets(workingDB); err != nil {
		return fmt.Errorf("create consolidation targets: %w", err)
	}

	// Step 2: Sentinel — if sessions already exist, we've already consolidated.
	var sessionCount int
	_ = workingDB.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessionCount)
	if sessionCount > 0 {
		logger.Debug("consolidation already complete", "sessions", sessionCount)
		return nil
	}

	// Step 3: Check if archive.db exists on disk.
	if _, err := os.Stat(archiveDBPath); os.IsNotExist(err) {
		logger.Debug("no archive.db to consolidate", "path", archiveDBPath)
		return nil
	}

	// Step 4: Attach archive.db and copy data.
	_, err := workingDB.Exec(`ATTACH DATABASE ? AS archive`, archiveDBPath)
	if err != nil {
		logger.Info("could not attach archive database", "path", archiveDBPath, "error", err)
		return nil
	}
	defer func() { _, _ = workingDB.Exec(`DETACH DATABASE archive`) }()

	start := time.Now()

	// Step 5: Copy each table that exists in archive.db.
	tables := []struct {
		name    string
		copySQL string
	}{
		{
			"sessions",
			`INSERT OR IGNORE INTO sessions
				(id, conversation_id, started_at, ended_at, end_reason,
				 message_count, summary, title, tags, metadata,
				 parent_session_id, parent_tool_call_id)
			SELECT id, conversation_id, started_at, ended_at, end_reason,
				message_count, summary, title, tags, metadata,
				parent_session_id, parent_tool_call_id
			FROM archive.sessions`,
		},
		{
			"archive_iterations",
			`INSERT OR IGNORE INTO archive_iterations
				(session_id, iteration_index, model, input_tokens, output_tokens,
				 tool_call_count, tool_call_ids, tools_offered, started_at,
				 duration_ms, has_tool_calls, break_reason)
			SELECT session_id, iteration_index, model, input_tokens, output_tokens,
				tool_call_count, tool_call_ids, tools_offered, started_at,
				duration_ms, has_tool_calls, break_reason
			FROM archive.archive_iterations`,
		},
		{
			"import_metadata",
			`INSERT OR IGNORE INTO import_metadata (source_id, source_type, archive_session_id, imported_at)
			SELECT source_id, source_type, archive_session_id, imported_at
			FROM archive.import_metadata`,
		},
		{
			"working_memory",
			`INSERT OR IGNORE INTO working_memory (conversation_id, content, updated_at)
			SELECT conversation_id, content, updated_at
			FROM archive.working_memory`,
		},
		{
			"delegations",
			`INSERT OR IGNORE INTO delegations
				(id, conversation_id, task, guidance, profile, model,
				 iterations, max_iterations, input_tokens, output_tokens,
				 exhausted, exhaust_reason, tools_called, messages,
				 result_content, started_at, completed_at, duration_ms, error)
			SELECT id, conversation_id, task, guidance, profile, model,
				iterations, max_iterations, input_tokens, output_tokens,
				exhausted, COALESCE(exhaust_reason, ''), tools_called, messages,
				result_content, started_at, completed_at, duration_ms, error
			FROM archive.delegations`,
		},
	}

	for _, tbl := range tables {
		// Check table exists in archive.
		var exists int
		_ = workingDB.QueryRow(
			`SELECT COUNT(*) FROM archive.sqlite_master WHERE type='table' AND name=?`,
			tbl.name,
		).Scan(&exists)
		if exists == 0 {
			logger.Debug("archive table not found, skipping", "table", tbl.name)
			continue
		}

		// Handle missing columns gracefully — old archive.db schemas may
		// not have all columns. Check for optional columns and fall back.
		copySQL := tbl.copySQL
		if tbl.name == "sessions" {
			// title, tags, metadata were added in v2; parent_* in v3.
			// Build the column list based on what exists in archive.
			hasTitle := archiveHasColumn(workingDB, "sessions", "title")
			hasParent := archiveHasColumn(workingDB, "sessions", "parent_session_id")

			switch {
			case hasParent:
				// Full schema — use default copySQL.
			case hasTitle:
				// v2 schema: has title/tags/metadata but no parent columns.
				copySQL = `INSERT OR IGNORE INTO sessions
					(id, conversation_id, started_at, ended_at, end_reason,
					 message_count, summary, title, tags, metadata)
				SELECT id, conversation_id, started_at, ended_at, end_reason,
					message_count, summary,
					COALESCE(title, ''), COALESCE(tags, ''), COALESCE(metadata, '')
				FROM archive.sessions`
			default:
				// Pre-v2 schema: only base columns.
				copySQL = `INSERT OR IGNORE INTO sessions
					(id, conversation_id, started_at, ended_at, end_reason,
					 message_count, summary)
				SELECT id, conversation_id, started_at, ended_at, end_reason,
					message_count, COALESCE(summary, '')
				FROM archive.sessions`
			}
		}
		if tbl.name == "archive_iterations" {
			// tool_call_ids and tools_offered were added later.
			if !archiveHasColumn(workingDB, "archive_iterations", "tool_call_ids") {
				copySQL = `INSERT OR IGNORE INTO archive_iterations
					(session_id, iteration_index, model, input_tokens, output_tokens,
					 tool_call_count, started_at, duration_ms, has_tool_calls, break_reason)
				SELECT session_id, iteration_index, model, input_tokens, output_tokens,
					tool_call_count, started_at, duration_ms, has_tool_calls, break_reason
				FROM archive.archive_iterations`
			}
		}

		result, err := workingDB.Exec(copySQL)
		if err != nil {
			return fmt.Errorf("copy %s: %w", tbl.name, err)
		}
		copied, _ := result.RowsAffected()
		if copied > 0 {
			logger.Info("consolidated table from archive",
				"table", tbl.name,
				"rows", copied,
			)
		}
	}

	logger.Info("database consolidation complete",
		"duration", time.Since(start).Round(time.Millisecond),
	)

	return nil
}

// createConsolidationTargets creates the tables that Phase 3 consolidation
// copies into. All statements are idempotent (CREATE TABLE IF NOT EXISTS).
func createConsolidationTargets(db *sql.DB) error {
	stmts := []string{
		// sessions
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL,
			started_at TIMESTAMP NOT NULL,
			ended_at TIMESTAMP,
			end_reason TEXT,
			message_count INTEGER DEFAULT 0,
			summary TEXT,
			title TEXT,
			tags TEXT,
			metadata TEXT,
			parent_session_id TEXT,
			parent_tool_call_id TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_conversation ON sessions(conversation_id, started_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_started ON sessions(started_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id, started_at)`,

		// archive_iterations (keep name to avoid churn; rename in Phase 4)
		`CREATE TABLE IF NOT EXISTS archive_iterations (
			session_id TEXT NOT NULL,
			iteration_index INTEGER NOT NULL,
			model TEXT NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			tool_call_count INTEGER NOT NULL DEFAULT 0,
			tool_call_ids TEXT,
			tools_offered TEXT,
			started_at TIMESTAMP NOT NULL,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			has_tool_calls BOOLEAN NOT NULL DEFAULT FALSE,
			break_reason TEXT,
			PRIMARY KEY (session_id, iteration_index)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_archive_iter_session ON archive_iterations(session_id, iteration_index)`,

		// import_metadata
		`CREATE TABLE IF NOT EXISTS import_metadata (
			source_id TEXT NOT NULL,
			source_type TEXT NOT NULL,
			archive_session_id TEXT NOT NULL,
			imported_at TIMESTAMP NOT NULL,
			PRIMARY KEY (source_id, source_type)
		)`,

		// working_memory
		`CREATE TABLE IF NOT EXISTS working_memory (
			conversation_id TEXT NOT NULL PRIMARY KEY,
			content TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,

		// delegations
		`CREATE TABLE IF NOT EXISTS delegations (
			id              TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL,
			task            TEXT NOT NULL,
			guidance        TEXT,
			profile         TEXT NOT NULL,
			model           TEXT NOT NULL,
			iterations      INTEGER NOT NULL,
			max_iterations  INTEGER NOT NULL,
			input_tokens    INTEGER NOT NULL,
			output_tokens   INTEGER NOT NULL,
			exhausted       BOOLEAN NOT NULL DEFAULT 0,
			exhaust_reason  TEXT,
			tools_called    TEXT,
			messages        TEXT,
			result_content  TEXT,
			started_at      TEXT NOT NULL,
			completed_at    TEXT NOT NULL,
			duration_ms     INTEGER NOT NULL,
			error           TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_delegations_conversation ON delegations(conversation_id, started_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_delegations_profile ON delegations(profile)`,
		`CREATE INDEX IF NOT EXISTS idx_delegations_started ON delegations(started_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_delegations_model ON delegations(model)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:40], err)
		}
	}
	return nil
}

// archiveHasColumn checks whether a column exists on a table in the attached
// archive database. The table must already be confirmed to exist.
func archiveHasColumn(db *sql.DB, table, column string) bool {
	if !isValidIdentifier(table) || !isValidIdentifier(column) {
		return false
	}
	_, err := db.Exec("SELECT " + column + " FROM archive." + table + " LIMIT 0")
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
