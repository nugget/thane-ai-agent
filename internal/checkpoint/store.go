package checkpoint

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
)

// Store handles checkpoint persistence.
type Store struct {
	db *sql.DB
}

// NewStore creates a checkpoint store using the given database.
func NewStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS checkpoints (
			id TEXT PRIMARY KEY,
			created_at TEXT NOT NULL,
			trigger TEXT NOT NULL,
			note TEXT,
			state_gz BLOB NOT NULL,
			byte_size INTEGER NOT NULL,
			message_count INTEGER NOT NULL,
			fact_count INTEGER NOT NULL
		);
		
		CREATE INDEX IF NOT EXISTS idx_checkpoints_created 
			ON checkpoints(created_at DESC);
		
		CREATE INDEX IF NOT EXISTS idx_checkpoints_trigger 
			ON checkpoints(trigger);
	`)
	return err
}

// Create saves a new checkpoint and returns it with ID populated.
func (s *Store) Create(trigger Trigger, note string, state *State) (*Checkpoint, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate id: %w", err)
	}

	// Serialize and compress state
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("marshal state: %w", err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(stateJSON); err != nil {
		return nil, fmt.Errorf("compress: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("close gzip: %w", err)
	}

	compressed := buf.Bytes()
	now := time.Now().UTC()

	// Count items
	msgCount := 0
	for _, conv := range state.Conversations {
		msgCount += len(conv.Messages)
	}
	factCount := len(state.Facts)

	cp := &Checkpoint{
		ID:           id,
		CreatedAt:    now,
		Trigger:      trigger,
		Note:         note,
		State:        state,
		ByteSize:     int64(len(compressed)),
		MessageCount: msgCount,
		FactCount:    factCount,
	}

	_, err = s.db.Exec(`
		INSERT INTO checkpoints (id, created_at, trigger, note, state_gz, byte_size, message_count, fact_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, id.String(), now.Format(time.RFC3339), trigger, note, compressed, len(compressed), msgCount, factCount)
	if err != nil {
		return nil, fmt.Errorf("insert: %w", err)
	}

	return cp, nil
}

// Get retrieves a checkpoint by ID, including full state.
func (s *Store) Get(id uuid.UUID) (*Checkpoint, error) {
	row := s.db.QueryRow(`
		SELECT id, created_at, trigger, note, state_gz, byte_size, message_count, fact_count
		FROM checkpoints WHERE id = ?
	`, id.String())

	return s.scanFull(row)
}

// List returns checkpoints ordered by creation time (newest first).
// Does not include full state to keep response small.
func (s *Store) List(limit int) ([]*Checkpoint, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.Query(`
		SELECT id, created_at, trigger, note, byte_size, message_count, fact_count
		FROM checkpoints
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var checkpoints []*Checkpoint
	for rows.Next() {
		cp, err := s.scanMeta(rows)
		if err != nil {
			return nil, err
		}
		checkpoints = append(checkpoints, cp)
	}
	return checkpoints, rows.Err()
}

// Latest returns the most recent checkpoint, or nil if none exist.
func (s *Store) Latest() (*Checkpoint, error) {
	row := s.db.QueryRow(`
		SELECT id, created_at, trigger, note, state_gz, byte_size, message_count, fact_count
		FROM checkpoints
		ORDER BY created_at DESC
		LIMIT 1
	`)
	
	cp, err := s.scanFull(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return cp, err
}

// Delete removes a checkpoint by ID.
func (s *Store) Delete(id uuid.UUID) error {
	result, err := s.db.Exec(`DELETE FROM checkpoints WHERE id = ?`, id.String())
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("checkpoint not found: %s", id)
	}
	return nil
}

// Prune removes checkpoints older than the given duration, keeping at least minKeep.
func (s *Store) Prune(olderThan time.Duration, minKeep int) (int, error) {
	cutoff := time.Now().UTC().Add(-olderThan)

	// First, count how many we have
	var total int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM checkpoints`).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}

	if total <= minKeep {
		return 0, nil
	}

	// Delete old ones, but keep at least minKeep
	result, err := s.db.Exec(`
		DELETE FROM checkpoints
		WHERE id IN (
			SELECT id FROM checkpoints
			WHERE created_at < ?
			ORDER BY created_at ASC
			LIMIT ?
		)
	`, cutoff.Format(time.RFC3339), total-minKeep)
	if err != nil {
		return 0, fmt.Errorf("delete: %w", err)
	}

	deleted, _ := result.RowsAffected()
	return int(deleted), nil
}

func (s *Store) scanFull(row *sql.Row) (*Checkpoint, error) {
	var cp Checkpoint
	var idStr, createdStr, triggerStr string
	var note sql.NullString
	var stateGz []byte

	err := row.Scan(&idStr, &createdStr, &triggerStr, &note, &stateGz, &cp.ByteSize, &cp.MessageCount, &cp.FactCount)
	if err != nil {
		return nil, err
	}

	cp.ID, _ = uuid.Parse(idStr)
	cp.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	cp.Trigger = Trigger(triggerStr)
	if note.Valid {
		cp.Note = note.String
	}

	// Decompress state
	gr, err := gzip.NewReader(bytes.NewReader(stateGz))
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer gr.Close()

	stateJSON, err := io.ReadAll(gr)
	if err != nil {
		return nil, fmt.Errorf("decompress: %w", err)
	}

	if err := json.Unmarshal(stateJSON, &cp.State); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}

	return &cp, nil
}

func (s *Store) scanMeta(rows *sql.Rows) (*Checkpoint, error) {
	var cp Checkpoint
	var idStr, createdStr, triggerStr string
	var note sql.NullString

	err := rows.Scan(&idStr, &createdStr, &triggerStr, &note, &cp.ByteSize, &cp.MessageCount, &cp.FactCount)
	if err != nil {
		return nil, err
	}

	cp.ID, _ = uuid.Parse(idStr)
	cp.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	cp.Trigger = Trigger(triggerStr)
	if note.Valid {
		cp.Note = note.String
	}

	return &cp, nil
}
