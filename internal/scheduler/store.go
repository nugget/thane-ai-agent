package scheduler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

// Store handles task and execution persistence.
type Store struct {
	db *sql.DB
}

// NewStore creates a scheduler store with SQLite backend.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		schedule_json TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		created_by TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS executions (
		id TEXT PRIMARY KEY,
		task_id TEXT NOT NULL,
		scheduled_at TEXT NOT NULL,
		started_at TEXT,
		completed_at TEXT,
		status TEXT NOT NULL,
		result TEXT,
		FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_executions_task_id ON executions(task_id);
	CREATE INDEX IF NOT EXISTS idx_executions_status ON executions(status);
	CREATE INDEX IF NOT EXISTS idx_executions_scheduled_at ON executions(scheduled_at);
	`

	_, err := s.db.Exec(schema)
	return err
}

// NewID generates a new UUIDv7.
func NewID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// Fallback to v4 if v7 fails
		return uuid.New().String()
	}
	return id.String()
}

// CreateTask persists a new task.
func (s *Store) CreateTask(t *Task) error {
	if t.ID == "" {
		t.ID = NewID()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	t.UpdatedAt = time.Now()

	scheduleJSON, err := json.Marshal(t.Schedule)
	if err != nil {
		return fmt.Errorf("marshal schedule: %w", err)
	}

	payloadJSON, err := json.Marshal(t.Payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	enabled := 0
	if t.Enabled {
		enabled = 1
	}

	_, err = s.db.Exec(`
		INSERT INTO tasks (id, name, schedule_json, payload_json, enabled, created_at, created_by, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, t.ID, t.Name, string(scheduleJSON), string(payloadJSON), enabled,
		t.CreatedAt.Format(time.RFC3339Nano), t.CreatedBy, t.UpdatedAt.Format(time.RFC3339Nano))

	return err
}

// GetTask retrieves a task by ID.
func (s *Store) GetTask(id string) (*Task, error) {
	row := s.db.QueryRow(`
		SELECT id, name, schedule_json, payload_json, enabled, created_at, created_by, updated_at
		FROM tasks WHERE id = ?
	`, id)

	return s.scanTask(row)
}

// GetTaskByName retrieves a task by its human-readable name.
// Returns nil, nil when no task with the given name exists.
func (s *Store) GetTaskByName(name string) (*Task, error) {
	row := s.db.QueryRow(`
		SELECT id, name, schedule_json, payload_json, enabled, created_at, created_by, updated_at
		FROM tasks WHERE name = ? LIMIT 1
	`, name)

	t, err := s.scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// ListTasks returns all tasks, optionally filtered by enabled status.
func (s *Store) ListTasks(enabledOnly bool) ([]*Task, error) {
	query := `SELECT id, name, schedule_json, payload_json, enabled, created_at, created_by, updated_at FROM tasks`
	if enabledOnly {
		query += ` WHERE enabled = 1`
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		t, err := s.scanTaskRow(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}

	return tasks, rows.Err()
}

// UpdateTask updates an existing task.
func (s *Store) UpdateTask(t *Task) error {
	t.UpdatedAt = time.Now()

	scheduleJSON, err := json.Marshal(t.Schedule)
	if err != nil {
		return fmt.Errorf("marshal schedule: %w", err)
	}

	payloadJSON, err := json.Marshal(t.Payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	enabled := 0
	if t.Enabled {
		enabled = 1
	}

	_, err = s.db.Exec(`
		UPDATE tasks SET name = ?, schedule_json = ?, payload_json = ?, enabled = ?, updated_at = ?
		WHERE id = ?
	`, t.Name, string(scheduleJSON), string(payloadJSON), enabled,
		t.UpdatedAt.Format(time.RFC3339Nano), t.ID)

	return err
}

// DeleteTask removes a task and its executions.
func (s *Store) DeleteTask(id string) error {
	_, err := s.db.Exec(`DELETE FROM tasks WHERE id = ?`, id)
	return err
}

// CreateExecution records a new execution.
func (s *Store) CreateExecution(e *Execution) error {
	if e.ID == "" {
		e.ID = NewID()
	}

	var startedAt, completedAt *string
	if e.StartedAt != nil {
		s := e.StartedAt.Format(time.RFC3339Nano)
		startedAt = &s
	}
	if e.CompletedAt != nil {
		s := e.CompletedAt.Format(time.RFC3339Nano)
		completedAt = &s
	}

	_, err := s.db.Exec(`
		INSERT INTO executions (id, task_id, scheduled_at, started_at, completed_at, status, result)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, e.ID, e.TaskID, e.ScheduledAt.Format(time.RFC3339Nano), startedAt, completedAt, e.Status, e.Result)

	return err
}

// UpdateExecution updates an execution record.
func (s *Store) UpdateExecution(e *Execution) error {
	var startedAt, completedAt *string
	if e.StartedAt != nil {
		s := e.StartedAt.Format(time.RFC3339Nano)
		startedAt = &s
	}
	if e.CompletedAt != nil {
		s := e.CompletedAt.Format(time.RFC3339Nano)
		completedAt = &s
	}

	_, err := s.db.Exec(`
		UPDATE executions SET started_at = ?, completed_at = ?, status = ?, result = ?
		WHERE id = ?
	`, startedAt, completedAt, e.Status, e.Result, e.ID)

	return err
}

// GetExecution retrieves an execution by ID.
func (s *Store) GetExecution(id string) (*Execution, error) {
	row := s.db.QueryRow(`
		SELECT id, task_id, scheduled_at, started_at, completed_at, status, result
		FROM executions WHERE id = ?
	`, id)

	return s.scanExecution(row)
}

// ListExecutions returns executions for a task.
func (s *Store) ListExecutions(taskID string, limit int) ([]*Execution, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := s.db.Query(`
		SELECT id, task_id, scheduled_at, started_at, completed_at, status, result
		FROM executions WHERE task_id = ?
		ORDER BY scheduled_at DESC LIMIT ?
	`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var execs []*Execution
	for rows.Next() {
		e, err := s.scanExecutionRow(rows)
		if err != nil {
			return nil, err
		}
		execs = append(execs, e)
	}

	return execs, rows.Err()
}

// GetPendingExecutions returns executions that need to run.
func (s *Store) GetPendingExecutions() ([]*Execution, error) {
	rows, err := s.db.Query(`
		SELECT id, task_id, scheduled_at, started_at, completed_at, status, result
		FROM executions WHERE status = ?
		ORDER BY scheduled_at ASC
	`, StatusPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var execs []*Execution
	for rows.Next() {
		e, err := s.scanExecutionRow(rows)
		if err != nil {
			return nil, err
		}
		execs = append(execs, e)
	}

	return execs, rows.Err()
}

// Helper scan functions

func (s *Store) scanTask(row *sql.Row) (*Task, error) {
	var t Task
	var scheduleJSON, payloadJSON string
	var enabled int
	var createdAt, updatedAt string

	err := row.Scan(&t.ID, &t.Name, &scheduleJSON, &payloadJSON, &enabled, &createdAt, &t.CreatedBy, &updatedAt)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(scheduleJSON), &t.Schedule); err != nil {
		return nil, fmt.Errorf("unmarshal schedule: %w", err)
	}
	if err := json.Unmarshal([]byte(payloadJSON), &t.Payload); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}

	t.Enabled = enabled == 1
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)

	return &t, nil
}

func (s *Store) scanTaskRow(rows *sql.Rows) (*Task, error) {
	var t Task
	var scheduleJSON, payloadJSON string
	var enabled int
	var createdAt, updatedAt string

	err := rows.Scan(&t.ID, &t.Name, &scheduleJSON, &payloadJSON, &enabled, &createdAt, &t.CreatedBy, &updatedAt)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(scheduleJSON), &t.Schedule); err != nil {
		return nil, fmt.Errorf("unmarshal schedule: %w", err)
	}
	if err := json.Unmarshal([]byte(payloadJSON), &t.Payload); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}

	t.Enabled = enabled == 1
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)

	return &t, nil
}

func (s *Store) scanExecution(row *sql.Row) (*Execution, error) {
	var e Execution
	var scheduledAt string
	var startedAt, completedAt, result sql.NullString

	err := row.Scan(&e.ID, &e.TaskID, &scheduledAt, &startedAt, &completedAt, &e.Status, &result)
	if err != nil {
		return nil, err
	}

	e.ScheduledAt, _ = time.Parse(time.RFC3339Nano, scheduledAt)
	if startedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, startedAt.String)
		e.StartedAt = &t
	}
	if completedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, completedAt.String)
		e.CompletedAt = &t
	}
	if result.Valid {
		e.Result = result.String
	}

	return &e, nil
}

func (s *Store) scanExecutionRow(rows *sql.Rows) (*Execution, error) {
	var e Execution
	var scheduledAt string
	var startedAt, completedAt, result sql.NullString

	err := rows.Scan(&e.ID, &e.TaskID, &scheduledAt, &startedAt, &completedAt, &e.Status, &result)
	if err != nil {
		return nil, err
	}

	e.ScheduledAt, _ = time.Parse(time.RFC3339Nano, scheduledAt)
	if startedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, startedAt.String)
		e.StartedAt = &t
	}
	if completedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, completedAt.String)
		e.CompletedAt = &t
	}
	if result.Valid {
		e.Result = result.String
	}

	return &e, nil
}
