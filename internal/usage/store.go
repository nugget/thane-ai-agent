// Package usage provides persistent token usage and cost tracking for
// LLM interactions. Records are append-only and indexed by timestamp,
// session, and conversation for efficient aggregation queries.
package usage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/config"
)

// Record represents a single LLM interaction's token usage and cost.
type Record struct {
	ID             string
	Timestamp      time.Time
	RequestID      string
	SessionID      string
	ConversationID string
	Model          string
	Provider       string // "anthropic", "ollama"
	InputTokens    int
	OutputTokens   int
	CostUSD        float64
	Role           string // "interactive", "delegate", "scheduled", "auxiliary"
	TaskName       string // "email_poll", "periodic_reflection", etc. (empty for interactive)
}

// Summary holds aggregated token usage and cost totals.
type Summary struct {
	TotalRecords      int
	TotalInputTokens  int64
	TotalOutputTokens int64
	TotalCostUSD      float64
}

// Store is an append-only SQLite store for token usage records. All
// public methods are safe for concurrent use (SQLite serializes writes).
type Store struct {
	db *sql.DB
}

// NewStore creates a usage store at the given database path. The schema
// is created automatically on first use.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open usage database: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate usage schema: %w", err)
	}

	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS usage_records (
		id              TEXT PRIMARY KEY,
		timestamp       TEXT NOT NULL,
		request_id      TEXT NOT NULL,
		session_id      TEXT,
		conversation_id TEXT,
		model           TEXT NOT NULL,
		provider        TEXT NOT NULL,
		input_tokens    INTEGER NOT NULL,
		output_tokens   INTEGER NOT NULL,
		cost_usd        REAL NOT NULL,
		role            TEXT NOT NULL,
		task_name       TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_usage_timestamp ON usage_records(timestamp);
	CREATE INDEX IF NOT EXISTS idx_usage_session ON usage_records(session_id);
	CREATE INDEX IF NOT EXISTS idx_usage_conversation ON usage_records(conversation_id);
	`
	_, err := s.db.Exec(schema)
	return err
}

// Record persists a usage record. If rec.ID is empty, a UUIDv7 is
// generated. The context is used for cancellation only.
func (s *Store) Record(ctx context.Context, rec Record) error {
	if rec.ID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate usage record ID: %w", err)
		}
		rec.ID = id.String()
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now()
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage_records
			(id, timestamp, request_id, session_id, conversation_id, model, provider,
			 input_tokens, output_tokens, cost_usd, role, task_name)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID,
		rec.Timestamp.UTC().Format(time.RFC3339),
		rec.RequestID,
		rec.SessionID,
		rec.ConversationID,
		rec.Model,
		rec.Provider,
		rec.InputTokens,
		rec.OutputTokens,
		rec.CostUSD,
		rec.Role,
		rec.TaskName,
	)
	if err != nil {
		return fmt.Errorf("insert usage record: %w", err)
	}
	return nil
}

// Summary returns aggregated totals for records within [start, end).
func (s *Store) Summary(start, end time.Time) (*Summary, error) {
	row := s.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cost_usd), 0)
		 FROM usage_records
		 WHERE timestamp >= ? AND timestamp < ?`,
		start.UTC().Format(time.RFC3339),
		end.UTC().Format(time.RFC3339),
	)

	var sum Summary
	if err := row.Scan(&sum.TotalRecords, &sum.TotalInputTokens, &sum.TotalOutputTokens, &sum.TotalCostUSD); err != nil {
		return nil, fmt.Errorf("query usage summary: %w", err)
	}
	return &sum, nil
}

// SummaryByModel returns per-model aggregated totals for records within [start, end).
func (s *Store) SummaryByModel(start, end time.Time) (map[string]*Summary, error) {
	return s.summaryGroupedBy("model", start, end)
}

// SummaryByRole returns per-role aggregated totals for records within [start, end).
func (s *Store) SummaryByRole(start, end time.Time) (map[string]*Summary, error) {
	return s.summaryGroupedBy("role", start, end)
}

// SummaryByTask returns per-task aggregated totals for records within [start, end).
// Records with empty task_name are grouped under the key "".
func (s *Store) SummaryByTask(start, end time.Time) (map[string]*Summary, error) {
	return s.summaryGroupedBy("task_name", start, end)
}

func (s *Store) summaryGroupedBy(column string, start, end time.Time) (map[string]*Summary, error) {
	// column is always a compile-time constant from our own methods,
	// never user input, so embedding it directly is safe.
	query := fmt.Sprintf(
		`SELECT COALESCE(%s, ''), COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cost_usd), 0)
		 FROM usage_records
		 WHERE timestamp >= ? AND timestamp < ?
		 GROUP BY %s
		 ORDER BY SUM(cost_usd) DESC`,
		column, column,
	)

	rows, err := s.db.Query(query,
		start.UTC().Format(time.RFC3339),
		end.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("query usage by %s: %w", column, err)
	}
	defer rows.Close()

	result := make(map[string]*Summary)
	for rows.Next() {
		var key string
		var sum Summary
		if err := rows.Scan(&key, &sum.TotalRecords, &sum.TotalInputTokens, &sum.TotalOutputTokens, &sum.TotalCostUSD); err != nil {
			return nil, fmt.Errorf("scan usage by %s: %w", column, err)
		}
		result[key] = &sum
	}
	return result, rows.Err()
}

// ComputeCost calculates the USD cost for a model's token usage based
// on the pricing table. Models not in the table are treated as free
// (local/Ollama models).
func ComputeCost(model string, inputTokens, outputTokens int, pricing map[string]config.PricingEntry) float64 {
	entry, ok := pricing[model]
	if !ok {
		return 0
	}
	cost := float64(inputTokens) / 1_000_000.0 * entry.InputPerMillion
	cost += float64(outputTokens) / 1_000_000.0 * entry.OutputPerMillion
	return cost
}
