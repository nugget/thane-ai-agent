// Package attachments provides a content-addressed attachment store backed
// by SHA-256 hashing and a SQLite metadata index. Duplicate files are stored
// only once on disk; metadata records track provenance per ingest, enabling
// queries by sender, channel, conversation, and content hash.
package attachments

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// Record is one metadata entry for an ingested attachment.
type Record struct {
	ID             string    // UUIDv4 primary key
	Hash           string    // hex-encoded SHA-256 of file content
	StorePath      string    // relative path in store: ab/cd/abcd...ef.jpg
	OriginalName   string    // filename as provided by sender
	ContentType    string    // MIME type (e.g. "image/jpeg")
	Size           int64     // file size in bytes
	Width          int       // image width in pixels (0 if not applicable)
	Height         int       // image height in pixels (0 if not applicable)
	Channel        string    // ingest channel ("signal", "email", etc.)
	Sender         string    // channel-specific sender identifier
	ConversationID string    // conversation the attachment belongs to
	ReceivedAt     time.Time // when the attachment was received

	// Vision analysis fields (populated by Analyzer).
	Description   string    // vision analysis text (empty = not analyzed)
	AnalyzedAt    time.Time // when analysis was performed (zero = not analyzed)
	AnalysisModel string    // model used for analysis
}

// IngestParams describes an attachment to be ingested into the store.
type IngestParams struct {
	Source         io.Reader // attachment data to read
	OriginalName   string    // original filename if known
	ContentType    string    // MIME type
	Size           int64     // declared size in bytes (informational)
	Width          int       // image width in pixels
	Height         int       // image height in pixels
	Channel        string    // source channel ("signal", "email", etc.)
	Sender         string    // channel-specific sender identifier
	ConversationID string    // conversation context
	ReceivedAt     time.Time // when the attachment was received
}

// Store manages content-addressed file storage with a SQLite metadata index.
type Store struct {
	db      *sql.DB
	rootDir string
	logger  *slog.Logger
}

// NewStore creates an attachment store rooted at rootDir with a SQLite
// metadata index at dbPath. The root directory is created if it does not
// exist.
func NewStore(dbPath string, rootDir string, logger *slog.Logger) (*Store, error) {
	db, err := database.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("attachments: open database: %w", err)
	}

	if logger == nil {
		logger = slog.Default()
	}

	s := &Store{
		db:      db,
		rootDir: rootDir,
		logger:  logger,
	}

	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("attachments: migrate: %w", err)
	}

	if err := os.MkdirAll(rootDir, 0o750); err != nil {
		db.Close()
		return nil, fmt.Errorf("attachments: create store dir: %w", err)
	}

	return s, nil
}

// migrate creates or upgrades the attachments table and indexes.
func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS attachments (
			id TEXT PRIMARY KEY,
			hash TEXT NOT NULL,
			store_path TEXT NOT NULL,
			original_name TEXT NOT NULL DEFAULT '',
			content_type TEXT NOT NULL DEFAULT '',
			size INTEGER NOT NULL DEFAULT 0,
			width INTEGER NOT NULL DEFAULT 0,
			height INTEGER NOT NULL DEFAULT 0,
			channel TEXT NOT NULL DEFAULT '',
			sender TEXT NOT NULL DEFAULT '',
			conversation_id TEXT NOT NULL DEFAULT '',
			received_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_attachments_hash ON attachments(hash);
		CREATE INDEX IF NOT EXISTS idx_attachments_conversation ON attachments(conversation_id);
		CREATE INDEX IF NOT EXISTS idx_attachments_channel_sender ON attachments(channel, sender);
	`)
	if err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	// Search index on received_at for ORDER BY in Search queries (added in phase 4).
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_attachments_received_at ON attachments(received_at)`); err != nil {
		return fmt.Errorf("create received_at index: %w", err)
	}

	// Vision analysis columns (added in phase 3).
	for _, col := range []struct{ name, typedef string }{
		{"description", "TEXT NOT NULL DEFAULT ''"},
		{"analyzed_at", "TEXT NOT NULL DEFAULT ''"},
		{"analysis_model", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := database.AddColumn(s.db, "attachments", col.name, col.typedef); err != nil {
			return fmt.Errorf("add column %s: %w", col.name, err)
		}
	}

	return nil
}

// Ingest reads the attachment from params.Source, computes its SHA-256
// hash, stores it content-addressed on disk, and records metadata in the
// index. If a file with the same hash already exists on disk, the file is
// not rewritten (dedup). A new metadata record is always created so that
// provenance is tracked per ingest. Returns the created [Record].
func (s *Store) Ingest(ctx context.Context, params IngestParams) (*Record, error) {
	// Write to a temp file while hashing the content.
	tmp, err := os.CreateTemp(s.rootDir, ".ingest-*")
	if err != nil {
		return nil, fmt.Errorf("attachments: create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	// Ensure temp file is cleaned up on any error path.
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpPath)
		}
	}()

	h := sha256.New()
	written, err := io.Copy(tmp, io.TeeReader(params.Source, h))
	if err != nil {
		tmp.Close()
		return nil, fmt.Errorf("attachments: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("attachments: close temp file: %w", err)
	}

	hash := hex.EncodeToString(h.Sum(nil))
	ext := extensionForType(params.ContentType, params.OriginalName)
	storePath := filepath.Join(hash[:2], hash[2:4], hash+ext)
	absPath := filepath.Join(s.rootDir, storePath)

	// Check if content already exists on disk (dedup).
	// Track whether this call created the blob so we can roll it back
	// if the metadata insert fails without removing pre-existing files.
	createdBlob := false
	if _, err := os.Stat(absPath); err == nil {
		// File already exists — let defer clean up the temp file.
	} else {
		// New content — move temp into place.
		if err := os.MkdirAll(filepath.Dir(absPath), 0o750); err != nil {
			return nil, fmt.Errorf("attachments: create hash dir: %w", err)
		}
		if err := os.Rename(tmpPath, absPath); err != nil {
			// Concurrent ingest of identical content: another goroutine
			// moved a file into place between our Stat and Rename. Treat
			// as a dedup hit.
			if errors.Is(err, os.ErrExist) {
				// Let defer clean up temp file.
			} else {
				return nil, fmt.Errorf("attachments: move to store: %w", err)
			}
		} else {
			cleanup = false // temp file successfully renamed; nothing to clean up.
			createdBlob = true
		}
	}

	rec := &Record{
		ID:             uuid.New().String(),
		Hash:           hash,
		StorePath:      storePath,
		OriginalName:   params.OriginalName,
		ContentType:    params.ContentType,
		Size:           written,
		Width:          params.Width,
		Height:         params.Height,
		Channel:        params.Channel,
		Sender:         params.Sender,
		ConversationID: params.ConversationID,
		ReceivedAt:     params.ReceivedAt,
	}

	if err := s.insertRecord(ctx, rec); err != nil {
		// Roll back newly-created blobs to avoid orphaned files.
		// Pre-existing blobs (dedup hits) are left alone.
		if createdBlob {
			os.Remove(absPath)
		}
		return nil, err
	}

	s.logger.Info("attachment ingested",
		"id", rec.ID,
		"hash", rec.Hash,
		"store_path", rec.StorePath,
		"size", rec.Size,
		"content_type", rec.ContentType,
		"channel", rec.Channel,
	)

	return rec, nil
}

// insertRecord writes a metadata record to the database.
func (s *Store) insertRecord(ctx context.Context, rec *Record) error {
	analyzedAt := ""
	if !rec.AnalyzedAt.IsZero() {
		analyzedAt = rec.AnalyzedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO attachments (
			id, hash, store_path, original_name, content_type,
			size, width, height, channel, sender,
			conversation_id, received_at,
			description, analyzed_at, analysis_model
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.Hash, rec.StorePath, rec.OriginalName, rec.ContentType,
		rec.Size, rec.Width, rec.Height, rec.Channel, rec.Sender,
		rec.ConversationID, rec.ReceivedAt.UTC().Format(time.RFC3339Nano),
		rec.Description, analyzedAt, rec.AnalysisModel,
	)
	if err != nil {
		return fmt.Errorf("attachments: insert record: %w", err)
	}
	return nil
}

// ByHash returns the first metadata record matching the given SHA-256 hash,
// or nil if no record exists.
func (s *Store) ByHash(ctx context.Context, hash string) (*Record, error) {
	return s.queryOne(ctx, `SELECT
		id, hash, store_path, original_name, content_type,
		size, width, height, channel, sender,
		conversation_id, received_at,
		description, analyzed_at, analysis_model
		FROM attachments WHERE hash = ? LIMIT 1`, hash)
}

// ByID returns the metadata record with the given UUID, or nil if not found.
func (s *Store) ByID(ctx context.Context, id string) (*Record, error) {
	return s.queryOne(ctx, `SELECT
		id, hash, store_path, original_name, content_type,
		size, width, height, channel, sender,
		conversation_id, received_at,
		description, analyzed_at, analysis_model
		FROM attachments WHERE id = ?`, id)
}

// AbsPath returns the absolute filesystem path for a stored attachment.
func (s *Store) AbsPath(rec *Record) string {
	return filepath.Join(s.rootDir, rec.StorePath)
}

// UpdateVision stores vision analysis results for an attachment record.
// An empty description is rejected — callers should only store
// meaningful analysis results. The analyzed_at timestamp is set
// automatically.
func (s *Store) UpdateVision(ctx context.Context, id, description, model string) error {
	if description == "" {
		return fmt.Errorf("attachments: update vision: empty description")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE attachments
		SET description = ?, analyzed_at = ?, analysis_model = ?
		WHERE id = ?`,
		description, time.Now().UTC().Format(time.RFC3339Nano), model, id,
	)
	if err != nil {
		return fmt.Errorf("attachments: update vision: %w", err)
	}
	return nil
}

// VisionByHash returns cached vision analysis for any record matching
// the given content hash. This enables reuse across dedup hits — if
// the same image was already analyzed for a different sender, the
// cached description is returned. Returns ok=false if no analyzed
// record exists for the hash. Non-ErrNoRows database errors are
// logged and treated as cache misses.
func (s *Store) VisionByHash(ctx context.Context, hash string) (description, model string, ok bool) {
	row := s.db.QueryRowContext(ctx, `
		SELECT description, analysis_model
		FROM attachments
		WHERE hash = ? AND analyzed_at != '' AND description != ''
		LIMIT 1`, hash)

	err := row.Scan(&description, &model)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.logger.Error("attachments: vision by hash scan failed",
				"hash", hash,
				"error", err,
			)
		}
		return "", "", false
	}
	return description, model, true
}

// SearchParams controls attachment listing and search queries.
type SearchParams struct {
	ConversationID string // filter to a specific conversation
	Channel        string // filter by channel ("signal", "email")
	Sender         string // filter by sender identifier
	ContentType    string // MIME prefix filter (e.g. "image/")
	Query          string // text search across name, description, sender
	Limit          int    // max results; 0 → 20, capped at 50
}

// Search returns attachment records matching the given filters, ordered
// by received_at descending (newest first). All filter fields are
// optional; an empty SearchParams returns the most recent attachments.
func (s *Store) Search(ctx context.Context, params SearchParams) ([]*Record, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	var conditions []string
	var args []any

	if params.ConversationID != "" {
		conditions = append(conditions, "conversation_id = ?")
		args = append(args, params.ConversationID)
	}
	if params.Channel != "" {
		conditions = append(conditions, "channel = ?")
		args = append(args, params.Channel)
	}
	if params.Sender != "" {
		conditions = append(conditions, "sender = ?")
		args = append(args, params.Sender)
	}
	if params.ContentType != "" {
		conditions = append(conditions, "content_type LIKE ?")
		args = append(args, params.ContentType+"%")
	}
	if params.Query != "" {
		conditions = append(conditions, "(original_name LIKE ? OR description LIKE ? OR sender LIKE ? OR channel LIKE ?)")
		q := "%" + params.Query + "%"
		args = append(args, q, q, q, q)
	}

	query := `SELECT
		id, hash, store_path, original_name, content_type,
		size, width, height, channel, sender,
		conversation_id, received_at,
		description, analyzed_at, analysis_model
		FROM attachments`
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY received_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("attachments: search: %w", err)
	}
	defer rows.Close()

	var records []*Record
	for rows.Next() {
		var rec Record
		var receivedAt, analyzedAt string
		err := rows.Scan(
			&rec.ID, &rec.Hash, &rec.StorePath, &rec.OriginalName, &rec.ContentType,
			&rec.Size, &rec.Width, &rec.Height, &rec.Channel, &rec.Sender,
			&rec.ConversationID, &receivedAt,
			&rec.Description, &analyzedAt, &rec.AnalysisModel,
		)
		if err != nil {
			return nil, fmt.Errorf("attachments: scan search result: %w", err)
		}
		rec.ReceivedAt, err = database.ParseTimestamp(receivedAt)
		if err != nil {
			return nil, fmt.Errorf("attachments: parse received_at %q: %w", receivedAt, err)
		}
		if analyzedAt != "" {
			rec.AnalyzedAt, err = database.ParseTimestamp(analyzedAt)
			if err != nil {
				return nil, fmt.Errorf("attachments: parse analyzed_at %q: %w", analyzedAt, err)
			}
		}
		records = append(records, &rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("attachments: search rows: %w", err)
	}
	return records, nil
}

// TelemetryStats returns aggregate attachment statistics for
// operational dashboards. The three counts (total records, total bytes,
// unique content hashes) are computed in a single query.
func (s *Store) TelemetryStats(ctx context.Context) (total, totalBytes, unique int64, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(size), 0), COUNT(DISTINCT hash) FROM attachments`,
	).Scan(&total, &totalBytes, &unique)
	return
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// queryOne executes a query expected to return zero or one Record rows.
func (s *Store) queryOne(ctx context.Context, query string, args ...any) (*Record, error) {
	row := s.db.QueryRowContext(ctx, query, args...)

	var rec Record
	var receivedAt, analyzedAt string

	err := row.Scan(
		&rec.ID, &rec.Hash, &rec.StorePath, &rec.OriginalName, &rec.ContentType,
		&rec.Size, &rec.Width, &rec.Height, &rec.Channel, &rec.Sender,
		&rec.ConversationID, &receivedAt,
		&rec.Description, &analyzedAt, &rec.AnalysisModel,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("attachments: scan record: %w", err)
	}

	rec.ReceivedAt, err = database.ParseTimestamp(receivedAt)
	if err != nil {
		return nil, fmt.Errorf("attachments: parse received_at %q: %w", receivedAt, err)
	}
	if analyzedAt != "" {
		rec.AnalyzedAt, err = database.ParseTimestamp(analyzedAt)
		if err != nil {
			return nil, fmt.Errorf("attachments: parse analyzed_at %q: %w", analyzedAt, err)
		}
	}

	return &rec, nil
}

// extensionForType returns a file extension (including the dot) for the
// given MIME type. Falls back to the extension from originalName, then
// to an empty string.
func extensionForType(contentType, originalName string) string {
	if contentType != "" {
		exts, _ := mime.ExtensionsByType(contentType)
		if len(exts) > 0 {
			// Prefer common short extensions over obscure ones.
			for _, preferred := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".mp4", ".pdf", ".ogg"} {
				for _, e := range exts {
					if e == preferred {
						return e
					}
				}
			}
			return exts[0]
		}
	}
	if originalName != "" {
		if ext := filepath.Ext(originalName); ext != "" {
			return strings.ToLower(ext)
		}
	}
	return ""
}
