package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "scheduler_test.db")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestGetTaskByName_NotFound(t *testing.T) {
	s := newTestStore(t)

	task, err := s.GetTaskByName("nonexistent")
	if err != nil {
		t.Fatalf("GetTaskByName error: %v", err)
	}
	if task != nil {
		t.Errorf("expected nil task, got %+v", task)
	}
}

func TestGetTaskByName_Found(t *testing.T) {
	s := newTestStore(t)

	// Create a task.
	want := &Task{
		Name: "test_task",
		Schedule: Schedule{
			Kind:  ScheduleEvery,
			Every: &Duration{Duration: 10 * time.Minute},
		},
		Payload: Payload{
			Kind: PayloadWake,
			Data: map[string]any{"message": "hello"},
		},
		Enabled:   true,
		CreatedBy: "test",
	}
	if err := s.CreateTask(want); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := s.GetTaskByName("test_task")
	if err != nil {
		t.Fatalf("GetTaskByName error: %v", err)
	}
	if got == nil {
		t.Fatal("expected task, got nil")
	}
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.Name != "test_task" {
		t.Errorf("Name = %q, want %q", got.Name, "test_task")
	}
	if !got.Enabled {
		t.Error("expected Enabled = true")
	}
}

func TestGetTaskByName_MultipleTasksReturnsCorrectOne(t *testing.T) {
	s := newTestStore(t)

	// Create two tasks with different names.
	task1 := &Task{
		Name:      "alpha",
		Schedule:  Schedule{Kind: ScheduleEvery, Every: &Duration{Duration: 5 * time.Minute}},
		Payload:   Payload{Kind: PayloadWake},
		Enabled:   true,
		CreatedBy: "test",
	}
	task2 := &Task{
		Name:      "beta",
		Schedule:  Schedule{Kind: ScheduleEvery, Every: &Duration{Duration: 10 * time.Minute}},
		Payload:   Payload{Kind: PayloadWake},
		Enabled:   true,
		CreatedBy: "test",
	}
	if err := s.CreateTask(task1); err != nil {
		t.Fatalf("CreateTask(alpha): %v", err)
	}
	if err := s.CreateTask(task2); err != nil {
		t.Fatalf("CreateTask(beta): %v", err)
	}

	got, err := s.GetTaskByName("beta")
	if err != nil {
		t.Fatalf("GetTaskByName error: %v", err)
	}
	if got == nil {
		t.Fatal("expected task, got nil")
	}
	if got.ID != task2.ID {
		t.Errorf("got task ID %q, want %q (beta)", got.ID, task2.ID)
	}

	// Verify alpha is not returned when querying for beta.
	if got.Name != "beta" {
		t.Errorf("Name = %q, want %q", got.Name, "beta")
	}
}

func TestGetTaskByName_DuplicateNamesReturnsError(t *testing.T) {
	s := newTestStore(t)

	// Create two tasks with the same name (shouldn't happen in practice).
	for i := range 2 {
		task := &Task{
			Name:      "duplicate",
			Schedule:  Schedule{Kind: ScheduleEvery, Every: &Duration{Duration: time.Duration(i+1) * time.Minute}},
			Payload:   Payload{Kind: PayloadWake},
			Enabled:   true,
			CreatedBy: "test",
		}
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("CreateTask(%d): %v", i, err)
		}
	}

	_, err := s.GetTaskByName("duplicate")
	if err == nil {
		t.Fatal("expected error for duplicate task names, got nil")
	}
	if !strings.Contains(err.Error(), "multiple tasks found") {
		t.Errorf("unexpected error: %v", err)
	}
}

// Ensure NewStore wires up a writable DB and applies the schema
// (sanity check for CI environments).
func TestNewStore_AppliesSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()

	if _, err := NewStore(db, nil); err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file was not created")
	}

	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='tasks'`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("schema did not create tasks table")
	}
}
