package scheduler

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "scheduler_test.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
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

// Ensure the test DB file is writable (sanity check for CI environments).
func TestNewStore_CreatesDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file was not created")
	}
}
