package logging

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

const (
	// archiveBatchSize is the number of request IDs fetched per batch
	// during an archive run. Keeping it bounded avoids loading the full
	// result set into memory for large backlogs.
	archiveBatchSize = 250
)

// Archiver exports old log_request_content rows to monthly JSONL files
// and removes them from the database. Each archived line is a
// self-contained JSON object (RequestDetail with nested ToolCalls and
// the resolved system prompt) so the flat files need no external
// index to be useful.
//
// Archive files are written to {dir}/archive/YYYY-MM.jsonl and are
// appended to on each run, so the operation is safe to repeat. If the
// process is interrupted after writing but before the database delete,
// some rows will be re-archived on the next run (harmless duplicate
// lines in the JSONL).
//
// log_prompts rows are never deleted — they are content-addressed and
// bounded by system prompt variation, not request volume.
type Archiver struct {
	db     *sql.DB
	dir    string
	logger *slog.Logger
}

// NewArchiver creates an Archiver that writes JSONL files directly into dir.
// The caller is responsible for resolving the directory path (see
// LoggingConfig.ContentArchiveDirPath for the default).
func NewArchiver(db *sql.DB, dir string, logger *slog.Logger) *Archiver {
	return &Archiver{db: db, dir: dir, logger: logger}
}

// Archive exports all log_request_content rows with created_at older
// than before to monthly JSONL files in a.dir, then deletes them (and
// their associated log_tool_content rows) from the database. Returns
// the number of requests archived.
func (a *Archiver) Archive(ctx context.Context, before time.Time) (int, error) {
	if err := os.MkdirAll(a.dir, 0o755); err != nil {
		return 0, fmt.Errorf("create archive dir: %w", err)
	}

	// open holds file handles keyed by "YYYY-MM" so each month's file
	// is opened at most once per Archive call.
	open := make(map[string]*monthFile)
	defer func() {
		for _, mf := range open {
			mf.close()
		}
	}()

	cutoff := before.UTC().Format(time.RFC3339Nano)
	total := 0

	for {
		ids, err := a.fetchBatch(ctx, cutoff)
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			break
		}

		archived, err := a.processBatch(ctx, ids, a.dir, open)
		total += archived
		if err != nil {
			return total, err
		}
	}

	return total, nil
}

// fetchBatch returns up to archiveBatchSize request IDs older than cutoff.
func (a *Archiver) fetchBatch(ctx context.Context, cutoff string) ([]string, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT request_id FROM log_request_content
		 WHERE created_at < ? ORDER BY created_at LIMIT ?`,
		cutoff, archiveBatchSize,
	)
	if err != nil {
		return nil, fmt.Errorf("fetch batch: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan request id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// processBatch fetches full detail for each ID, writes to the
// appropriate monthly file, and deletes from the database.
func (a *Archiver) processBatch(ctx context.Context, ids []string, archiveDir string, open map[string]*monthFile) (int, error) {
	// Write phase: fetch and emit each record.
	for _, id := range ids {
		rd, err := QueryRequestDetail(a.db, id)
		if err != nil {
			return 0, fmt.Errorf("query request %s: %w", id, err)
		}
		if rd == nil {
			// Row disappeared between fetch and query — skip.
			continue
		}

		monthKey, err := monthKeyFor(rd.CreatedAt)
		if err != nil {
			a.logger.Warn("archive: cannot parse created_at, using fallback month",
				"request_id", id,
				"created_at", rd.CreatedAt,
				"error", err,
			)
			monthKey = "unknown"
		}

		mf, ok := open[monthKey]
		if !ok {
			path := filepath.Join(archiveDir, monthKey+".jsonl")
			mf, err = openMonthFile(path)
			if err != nil {
				return 0, fmt.Errorf("open archive file %s: %w", path, err)
			}
			open[monthKey] = mf
		}

		if err := mf.write(rd); err != nil {
			return 0, fmt.Errorf("write archive record %s: %w", id, err)
		}
	}

	// Flush all open files before deleting from DB so we don't lose
	// data if the process is interrupted.
	for _, mf := range open {
		if err := mf.flush(); err != nil {
			return 0, fmt.Errorf("flush archive file: %w", err)
		}
	}

	// Delete phase: remove written rows from the database.
	if err := a.deleteBatch(ctx, ids); err != nil {
		return 0, err
	}

	return len(ids), nil
}

// deleteBatch removes the given request IDs from log_request_content
// and log_tool_content in a single transaction.
func (a *Archiver) deleteBatch(ctx context.Context, ids []string) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM log_tool_content WHERE request_id = ?`, id); err != nil {
			return fmt.Errorf("delete tool rows for %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM log_request_content WHERE request_id = ?`, id); err != nil {
			return fmt.Errorf("delete request row for %s: %w", id, err)
		}
	}

	return tx.Commit()
}

// monthFile wraps a buffered writer for a single monthly JSONL file.
type monthFile struct {
	f   *os.File
	bw  *bufio.Writer
	enc *json.Encoder
}

// openMonthFile opens path in append+create mode and returns a monthFile.
func openMonthFile(path string) (*monthFile, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	bw := bufio.NewWriterSize(f, 256*1024)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	return &monthFile{f: f, bw: bw, enc: enc}, nil
}

// write encodes rd as a single JSON line.
func (mf *monthFile) write(rd *RequestDetail) error {
	return mf.enc.Encode(rd)
}

// flush flushes the buffer and syncs the underlying file.
func (mf *monthFile) flush() error {
	if err := mf.bw.Flush(); err != nil {
		return err
	}
	return mf.f.Sync()
}

// close flushes and closes the file.
func (mf *monthFile) close() {
	_ = mf.flush()
	_ = mf.f.Close()
}

// monthKeyFor parses a created_at string (RFC3339 or RFC3339Nano) and
// returns a "YYYY-MM" string for use as an archive file name.
func monthKeyFor(createdAt string) (string, error) {
	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		t, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return "", fmt.Errorf("parse timestamp %q: %w", createdAt, err)
		}
	}
	return t.UTC().Format("2006-01"), nil
}
