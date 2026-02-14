package delegate

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/llm"
)

// DelegationRecord represents a persisted delegation execution for replay
// and model evaluation.
type DelegationRecord struct {
	ID             string         `json:"id"`
	ConversationID string         `json:"conversation_id"`
	Task           string         `json:"task"`
	Guidance       string         `json:"guidance,omitempty"`
	Profile        string         `json:"profile"`
	Model          string         `json:"model"`
	Iterations     int            `json:"iterations"`
	MaxIterations  int            `json:"max_iterations"`
	InputTokens    int            `json:"input_tokens"`
	OutputTokens   int            `json:"output_tokens"`
	Exhausted      bool           `json:"exhausted"`
	ExhaustReason  string         `json:"exhaust_reason,omitempty"`
	ToolsCalled    map[string]int `json:"tools_called,omitempty"`
	Messages       []llm.Message  `json:"messages,omitempty"`
	ResultContent  string         `json:"result_content"`
	StartedAt      time.Time      `json:"started_at"`
	CompletedAt    time.Time      `json:"completed_at"`
	DurationMs     int64          `json:"duration_ms"`
	Error          string         `json:"error,omitempty"`
}

// DelegationStore persists delegation execution records in the archive
// database. It shares the same [sql.DB] connection as the archive store
// (typically archive.db) and creates its own table on initialization.
type DelegationStore struct {
	db *sql.DB
}

// NewDelegationStore creates a delegation store using the given database
// connection (typically from [memory.ArchiveStore.DB]). It creates the
// delegations table if it does not already exist.
func NewDelegationStore(db *sql.DB) (*DelegationStore, error) {
	s := &DelegationStore{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("delegation store migrate: %w", err)
	}
	return s, nil
}

func (s *DelegationStore) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS delegations (
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
		);

		CREATE INDEX IF NOT EXISTS idx_delegations_conversation
			ON delegations(conversation_id, started_at DESC);
		CREATE INDEX IF NOT EXISTS idx_delegations_profile
			ON delegations(profile);
		CREATE INDEX IF NOT EXISTS idx_delegations_started
			ON delegations(started_at DESC);
		CREATE INDEX IF NOT EXISTS idx_delegations_model
			ON delegations(model);
	`)
	if err != nil {
		return err
	}

	// Add exhaust_reason column for databases created before this migration.
	if has, err := s.hasColumn("delegations", "exhaust_reason"); err != nil {
		return fmt.Errorf("check exhaust_reason column: %w", err)
	} else if !has {
		if _, err := s.db.Exec(`ALTER TABLE delegations ADD COLUMN exhaust_reason TEXT`); err != nil {
			return fmt.Errorf("add exhaust_reason column: %w", err)
		}
	}
	return nil
}

// hasColumn checks whether a column exists on the given table using
// PRAGMA table_info, avoiding silent ALTER TABLE failures.
func (s *DelegationStore) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dfltValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Record inserts a delegation execution record into the database.
func (s *DelegationStore) Record(rec *DelegationRecord) error {
	toolsJSON, err := json.Marshal(rec.ToolsCalled)
	if err != nil {
		return fmt.Errorf("marshal tools_called: %w", err)
	}

	msgsJSON, err := json.Marshal(rec.Messages)
	if err != nil {
		return fmt.Errorf("marshal messages: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO delegations (
			id, conversation_id, task, guidance, profile, model,
			iterations, max_iterations, input_tokens, output_tokens,
			exhausted, exhaust_reason, tools_called, messages, result_content,
			started_at, completed_at, duration_ms, error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.ConversationID, rec.Task, rec.Guidance,
		rec.Profile, rec.Model,
		rec.Iterations, rec.MaxIterations,
		rec.InputTokens, rec.OutputTokens,
		rec.Exhausted, rec.ExhaustReason, string(toolsJSON), string(msgsJSON),
		rec.ResultContent,
		rec.StartedAt.Format(time.RFC3339Nano),
		rec.CompletedAt.Format(time.RFC3339Nano),
		rec.DurationMs, rec.Error,
	)
	return err
}

// Get retrieves a single delegation record by ID.
func (s *DelegationStore) Get(id string) (*DelegationRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, conversation_id, task, guidance, profile, model,
			iterations, max_iterations, input_tokens, output_tokens,
			exhausted, exhaust_reason, tools_called, messages, result_content,
			started_at, completed_at, duration_ms, error
		FROM delegations WHERE id = ?`, id)
	return scanRecord(row)
}

// List returns delegation records ordered newest-first. If limit is 0,
// all records are returned.
func (s *DelegationStore) List(limit int) ([]*DelegationRecord, error) {
	query := `
		SELECT id, conversation_id, task, guidance, profile, model,
			iterations, max_iterations, input_tokens, output_tokens,
			exhausted, exhaust_reason, tools_called, messages, result_content,
			started_at, completed_at, duration_ms, error
		FROM delegations ORDER BY started_at DESC`
	var args []any
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*DelegationRecord
	for rows.Next() {
		rec, err := scanRecordRows(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

// scanner abstracts *sql.Row and *sql.Rows for shared scanning logic.
type scanner interface {
	Scan(dest ...any) error
}

func scanInto(s scanner) (*DelegationRecord, error) {
	var rec DelegationRecord
	var guidance, exhaustReason, toolsJSON, msgsJSON, resultContent, errStr sql.NullString
	var startedAt, completedAt string

	err := s.Scan(
		&rec.ID, &rec.ConversationID, &rec.Task, &guidance,
		&rec.Profile, &rec.Model,
		&rec.Iterations, &rec.MaxIterations,
		&rec.InputTokens, &rec.OutputTokens,
		&rec.Exhausted, &exhaustReason, &toolsJSON, &msgsJSON, &resultContent,
		&startedAt, &completedAt,
		&rec.DurationMs, &errStr,
	)
	if err != nil {
		return nil, err
	}

	rec.Guidance = guidance.String
	rec.ExhaustReason = exhaustReason.String
	rec.ResultContent = resultContent.String
	rec.Error = errStr.String

	rec.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
	rec.CompletedAt, _ = time.Parse(time.RFC3339Nano, completedAt)

	if toolsJSON.Valid && toolsJSON.String != "" {
		_ = json.Unmarshal([]byte(toolsJSON.String), &rec.ToolsCalled)
	}
	if msgsJSON.Valid && msgsJSON.String != "" {
		_ = json.Unmarshal([]byte(msgsJSON.String), &rec.Messages)
	}

	return &rec, nil
}

func scanRecord(row *sql.Row) (*DelegationRecord, error) {
	return scanInto(row)
}

func scanRecordRows(rows *sql.Rows) (*DelegationRecord, error) {
	return scanInto(rows)
}

// ExtractToolsCalled scans a message history and returns a map of tool
// names to invocation counts.
func ExtractToolsCalled(messages []llm.Message) map[string]int {
	counts := make(map[string]int)
	for _, msg := range messages {
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name != "" {
				counts[tc.Function.Name]++
			}
		}
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}
