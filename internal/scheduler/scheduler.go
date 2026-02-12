package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// ExecuteFunc is called when a task fires.
type ExecuteFunc func(ctx context.Context, task *Task, execution *Execution) error

// Scheduler manages task scheduling and execution.
type Scheduler struct {
	logger  *slog.Logger
	store   *Store
	execute ExecuteFunc

	mu      sync.Mutex
	timers  map[string]*time.Timer // taskID -> timer
	running bool
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

// New creates a new scheduler.
func New(logger *slog.Logger, store *Store, execute ExecuteFunc) *Scheduler {
	return &Scheduler{
		logger:  logger,
		store:   store,
		execute: execute,
		timers:  make(map[string]*time.Timer),
		stopCh:  make(chan struct{}),
	}
}

// Start begins the scheduler, loading tasks and setting up timers.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = true
	s.mu.Unlock()

	s.logger.Debug("scheduler starting")

	// Load and schedule all enabled tasks
	tasks, err := s.store.ListTasks(true)
	if err != nil {
		return err
	}

	for _, task := range tasks {
		s.scheduleTask(task)
	}

	s.logger.Debug("scheduler started", "tasks", len(tasks))

	// Check for any missed executions on startup
	s.checkMissedExecutions(ctx)

	return nil
}

// Stop halts the scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false

	// Cancel all timers
	for id, timer := range s.timers {
		timer.Stop()
		delete(s.timers, id)
	}

	close(s.stopCh)
	s.mu.Unlock()

	s.wg.Wait()
	s.logger.Info("scheduler stopped")
}

// CreateTask adds a new task and schedules it.
func (s *Scheduler) CreateTask(task *Task) error {
	if err := s.store.CreateTask(task); err != nil {
		return err
	}

	if task.Enabled {
		s.scheduleTask(task)
	}

	s.logger.Info("task created",
		"id", task.ID,
		"name", task.Name,
		"schedule", task.Schedule.Kind,
	)

	return nil
}

// UpdateTask modifies a task and reschedules it.
func (s *Scheduler) UpdateTask(task *Task) error {
	if err := s.store.UpdateTask(task); err != nil {
		return err
	}

	// Cancel existing timer
	s.cancelTimer(task.ID)

	// Reschedule if enabled
	if task.Enabled {
		s.scheduleTask(task)
	}

	s.logger.Info("task updated", "id", task.ID, "name", task.Name)

	return nil
}

// DeleteTask removes a task.
func (s *Scheduler) DeleteTask(id string) error {
	s.cancelTimer(id)

	if err := s.store.DeleteTask(id); err != nil {
		return err
	}

	s.logger.Info("task deleted", "id", id)
	return nil
}

// GetTask retrieves a task by ID.
func (s *Scheduler) GetTask(id string) (*Task, error) {
	return s.store.GetTask(id)
}

// ListTasks returns all tasks.
func (s *Scheduler) ListTasks(enabledOnly bool) ([]*Task, error) {
	return s.store.ListTasks(enabledOnly)
}

// GetAllTasks returns all tasks for checkpointing.
func (s *Scheduler) GetAllTasks() ([]*Task, error) {
	return s.store.ListTasks(false) // Include disabled tasks
}

// GetTaskExecutions returns execution history for a task.
func (s *Scheduler) GetTaskExecutions(taskID string, limit int) ([]*Execution, error) {
	return s.store.ListExecutions(taskID, limit)
}

// TriggerTask immediately executes a task (bypassing schedule).
func (s *Scheduler) TriggerTask(ctx context.Context, taskID string) (*Execution, error) {
	task, err := s.store.GetTask(taskID)
	if err != nil {
		return nil, err
	}

	return s.executeTask(ctx, task, time.Now())
}

// scheduleTask sets up a timer for the next execution.
func (s *Scheduler) scheduleTask(task *Task) {
	next, ok := task.NextRun(time.Now())
	if !ok {
		s.logger.Debug("task has no future runs", "id", task.ID, "name", task.Name)
		return
	}

	delay := time.Until(next)
	if delay < 0 {
		delay = 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Cancel existing timer if any
	if timer, exists := s.timers[task.ID]; exists {
		timer.Stop()
	}

	s.timers[task.ID] = time.AfterFunc(delay, func() {
		s.onTaskFire(task.ID)
	})

	s.logger.Debug("task scheduled",
		"id", task.ID,
		"name", task.Name,
		"next", next,
		"delay", delay,
	)
}

// onTaskFire is called when a task's timer fires.
func (s *Scheduler) onTaskFire(taskID string) {
	s.wg.Add(1)
	defer s.wg.Done()

	// Check if we're still running
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	delete(s.timers, taskID)
	s.mu.Unlock()

	// Get fresh task data
	task, err := s.store.GetTask(taskID)
	if err != nil {
		s.logger.Error("failed to get task for execution", "id", taskID, "error", err)
		return
	}

	if !task.Enabled {
		return
	}

	// Execute
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	_, err = s.executeTask(ctx, task, time.Now())
	if err != nil {
		s.logger.Error("task execution failed", "id", taskID, "error", err)
	}

	// Reschedule for repeating tasks
	if task.Schedule.Kind != ScheduleAt {
		s.scheduleTask(task)
	}
}

// executeTask runs a task and records the execution.
func (s *Scheduler) executeTask(ctx context.Context, task *Task, scheduledAt time.Time) (*Execution, error) {
	// Create execution record
	exec := &Execution{
		ID:          NewID(),
		TaskID:      task.ID,
		ScheduledAt: scheduledAt,
		Status:      StatusRunning,
	}
	now := time.Now()
	exec.StartedAt = &now

	if err := s.store.CreateExecution(exec); err != nil {
		return nil, err
	}

	s.logger.Info("executing task",
		"task_id", task.ID,
		"task_name", task.Name,
		"execution_id", exec.ID,
	)

	// Run the execution callback
	var execErr error
	if s.execute != nil {
		execErr = s.execute(ctx, task, exec)
	}

	// Update execution record
	completed := time.Now()
	exec.CompletedAt = &completed

	if execErr != nil {
		exec.Status = StatusFailed
		exec.Result = execErr.Error()
	} else {
		exec.Status = StatusCompleted
		exec.Result = "success"
	}

	if err := s.store.UpdateExecution(exec); err != nil {
		s.logger.Error("failed to update execution", "id", exec.ID, "error", err)
	}

	s.logger.Info("task execution completed",
		"task_id", task.ID,
		"execution_id", exec.ID,
		"status", exec.Status,
		"duration", completed.Sub(*exec.StartedAt),
	)

	return exec, execErr
}

// cancelTimer stops and removes a task's timer.
func (s *Scheduler) cancelTimer(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if timer, exists := s.timers[taskID]; exists {
		timer.Stop()
		delete(s.timers, taskID)
	}
}

// checkMissedExecutions handles tasks that should have run while we were down.
func (s *Scheduler) checkMissedExecutions(ctx context.Context) {
	pending, err := s.store.GetPendingExecutions()
	if err != nil {
		s.logger.Error("failed to get pending executions", "error", err)
		return
	}

	for _, exec := range pending {
		if time.Since(exec.ScheduledAt) > 24*time.Hour {
			// Too old, skip it
			exec.Status = StatusSkipped
			exec.Result = "missed execution window (>24h)"
			_ = s.store.UpdateExecution(exec)
			s.logger.Info("skipped stale execution", "id", exec.ID, "scheduled", exec.ScheduledAt)
		} else {
			// Run it now
			task, err := s.store.GetTask(exec.TaskID)
			if err != nil {
				continue
			}
			s.logger.Info("catching up missed execution", "task", task.Name, "scheduled", exec.ScheduledAt)
			// Mark this one as skipped and create a new one
			exec.Status = StatusSkipped
			exec.Result = "replaced by catch-up execution"
			_ = s.store.UpdateExecution(exec)
			_, _ = s.executeTask(ctx, task, exec.ScheduledAt)
		}
	}
}

// Stats returns scheduler statistics.
func (s *Scheduler) Stats() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	tasks, _ := s.store.ListTasks(false)
	enabled := 0
	for _, t := range tasks {
		if t.Enabled {
			enabled++
		}
	}

	return map[string]any{
		"running":       s.running,
		"total_tasks":   len(tasks),
		"enabled_tasks": enabled,
		"active_timers": len(s.timers),
	}
}
