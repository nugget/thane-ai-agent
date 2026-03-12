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
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/database"
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
	if _, err := os.Stat(absPath); err == nil {
		// File already exists — remove temp, reuse existing.
		os.Remove(tmpPath)
		cleanup = false
	} else {
		// New content — move temp into place.
		if err := os.MkdirAll(filepath.Dir(absPath), 0o750); err != nil {
			return nil, fmt.Errorf("attachments: create hash dir: %w", err)
		}
		if err := os.Rename(tmpPath, absPath); err != nil {
			return nil, fmt.Errorf("attachments: move to store: %w", err)
		}
		cleanup = false
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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO attachments (
			id, hash, store_path, original_name, content_type,
			size, width, height, channel, sender,
			conversation_id, received_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.Hash, rec.StorePath, rec.OriginalName, rec.ContentType,
		rec.Size, rec.Width, rec.Height, rec.Channel, rec.Sender,
		rec.ConversationID, rec.ReceivedAt.UTC().Format(time.RFC3339Nano),
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
		conversation_id, received_at
		FROM attachments WHERE hash = ? LIMIT 1`, hash)
}

// ByID returns the metadata record with the given UUID, or nil if not found.
func (s *Store) ByID(ctx context.Context, id string) (*Record, error) {
	return s.queryOne(ctx, `SELECT
		id, hash, store_path, original_name, content_type,
		size, width, height, channel, sender,
		conversation_id, received_at
		FROM attachments WHERE id = ?`, id)
}

// AbsPath returns the absolute filesystem path for a stored attachment.
func (s *Store) AbsPath(rec *Record) string {
	return filepath.Join(s.rootDir, rec.StorePath)
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// queryOne executes a query expected to return zero or one Record rows.
func (s *Store) queryOne(ctx context.Context, query string, args ...any) (*Record, error) {
	row := s.db.QueryRowContext(ctx, query, args...)

	var rec Record
	var receivedAt string

	err := row.Scan(
		&rec.ID, &rec.Hash, &rec.StorePath, &rec.OriginalName, &rec.ContentType,
		&rec.Size, &rec.Width, &rec.Height, &rec.Channel, &rec.Sender,
		&rec.ConversationID, &receivedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("attachments: scan record: %w", err)
	}

	rec.ReceivedAt, err = time.Parse(time.RFC3339Nano, receivedAt)
	if err != nil {
		return nil, fmt.Errorf("attachments: parse received_at %q: %w", receivedAt, err)
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
