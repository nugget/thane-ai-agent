package media

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/database"
)

// Engagement records a media analysis event — what was analyzed, where
// the output was saved, and metadata for future interest prediction.
type Engagement struct {
	ID            string
	EntryURL      string
	FeedID        string
	AnalysisPath  string
	AnalysisDepth string
	Topics        []string
	TrustZone     string
	QualityScore  float64
	Engaged       bool
	AnalyzedAt    time.Time
	SessionID     string
}

// MediaStore persists media engagement records in SQLite. It tracks
// which content has been analyzed, enabling deduplication and (in
// future) interest prediction from engagement history.
type MediaStore struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewMediaStore opens (or creates) the engagement database at dbPath
// and runs migrations.
func NewMediaStore(dbPath string, logger *slog.Logger) (*MediaStore, error) {
	if logger == nil {
		logger = slog.Default()
	}
	db, err := database.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open media store: %w", err)
	}
	s := &MediaStore{db: db, logger: logger}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate media store: %w", err)
	}
	return s, nil
}

func (s *MediaStore) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS media_engagement (
			id             TEXT PRIMARY KEY,
			entry_url      TEXT NOT NULL,
			feed_id        TEXT,
			analysis_path  TEXT,
			analysis_depth TEXT,
			topics         TEXT,
			trust_zone     TEXT,
			quality_score  REAL,
			engaged        BOOLEAN DEFAULT FALSE,
			analyzed_at    TEXT NOT NULL,
			session_id     TEXT
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_engagement_url_feed ON media_engagement(entry_url, feed_id);
		CREATE INDEX IF NOT EXISTS idx_engagement_feed ON media_engagement(feed_id);
	`)
	return err
}

// RecordAnalysis inserts an engagement record. If the Engagement has no
// ID, a new UUIDv7 is generated. If AnalyzedAt is zero, the current
// time is used.
func (s *MediaStore) RecordAnalysis(ctx context.Context, e *Engagement) error {
	if e.ID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate engagement ID: %w", err)
		}
		e.ID = id.String()
	}
	if e.AnalyzedAt.IsZero() {
		e.AnalyzedAt = time.Now().UTC()
	}

	topicsJSON, err := json.Marshal(e.Topics)
	if err != nil {
		return fmt.Errorf("marshal topics: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO media_engagement
			(id, entry_url, feed_id, analysis_path, analysis_depth, topics,
			 trust_zone, quality_score, engaged, analyzed_at, session_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		e.ID, e.EntryURL, e.FeedID, e.AnalysisPath, e.AnalysisDepth,
		string(topicsJSON), e.TrustZone, e.QualityScore, e.Engaged,
		e.AnalyzedAt.Format(time.RFC3339), e.SessionID,
	)
	if err != nil {
		return fmt.Errorf("insert engagement: %w", err)
	}
	return nil
}

// HasBeenAnalyzed reports whether the given entry URL has already been
// analyzed. Used to avoid duplicate analysis on re-polls.
func (s *MediaStore) HasBeenAnalyzed(ctx context.Context, entryURL string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM media_engagement WHERE entry_url = ?
	`, entryURL).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check engagement: %w", err)
	}
	return count > 0, nil
}

// Close closes the underlying database connection.
func (s *MediaStore) Close() error {
	return s.db.Close()
}
