package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/scheduler"
)

// SchedulerReader exposes read access to the scheduler's durable task and
// execution store for the /v1/schedules endpoints. It is the subset of
// *scheduler.Scheduler the API needs and is satisfied by that scheduler
// directly — surfacing the previously internal-only scheduler so operators
// can see what is scheduled to fire and what has already fired.
type SchedulerReader interface {
	ListTasks(enabledOnly bool) ([]*scheduler.Task, error)
	GetTask(id string) (*scheduler.Task, error)
	GetTaskExecutions(taskID string, limit int) ([]*scheduler.Execution, error)
}

// UseScheduler wires the scheduler that backs /v1/schedules.
func (s *Server) UseScheduler(r SchedulerReader) { s.schedulerReader = r }

// scheduleView augments a scheduled task with its next computed fire time.
// NextRun is derived from the schedule (not persisted) and is omitted for a
// task with no future run, e.g. a one-shot whose time has already passed.
type scheduleView struct {
	*scheduler.Task
	NextRun *time.Time `json:"next_run,omitempty"`
}

// newScheduleView builds a view for t, computing its next fire time from now.
func newScheduleView(t *scheduler.Task) scheduleView {
	v := scheduleView{Task: t}
	if next, ok := t.NextRun(time.Now()); ok {
		v.NextRun = &next
	}
	return v
}

// handleSchedules returns the scheduled-task registry as a bare JSON array,
// newest first, each augmented with its next fire time. An optional
// ?enabled=true filter narrows to enabled tasks. [GET /v1/schedules]
func (s *Server) handleSchedules(w http.ResponseWriter, r *http.Request) {
	if s.schedulerReader == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "scheduler not configured")
		return
	}
	enabledOnly := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("enabled")), "true")
	tasks, err := s.schedulerReader.ListTasks(enabledOnly)
	if err != nil {
		s.logger.Warn("schedule list query failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "query failed")
		return
	}
	views := make([]scheduleView, 0, len(tasks))
	for _, t := range tasks {
		views = append(views, newScheduleView(t))
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, views, s.logger)
}

// handleSchedule returns one scheduled task with its next fire time.
// [GET /v1/schedules/{id}]
func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	task, ok := s.lookupSchedule(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, newScheduleView(task), s.logger)
}

// handleScheduleExecutions returns a task's execution history as a bare JSON
// array, newest first (default 50, max 200). [GET /v1/schedules/{id}/executions]
func (s *Server) handleScheduleExecutions(w http.ResponseWriter, r *http.Request) {
	task, ok := s.lookupSchedule(w, r)
	if !ok {
		return
	}
	execs, err := s.schedulerReader.GetTaskExecutions(task.ID, parseLogLimit(r.URL.Query().Get("limit")))
	if err != nil {
		s.logger.Warn("schedule executions query failed", "task_id", task.ID, "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "query failed")
		return
	}
	// Always encode an array (never null) so the response shape is stable.
	if execs == nil {
		execs = []*scheduler.Execution{}
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, execs, s.logger)
}

// lookupSchedule resolves the {id} path value to a task, writing the
// appropriate error response and returning ok=false when it cannot.
func (s *Server) lookupSchedule(w http.ResponseWriter, r *http.Request) (*scheduler.Task, bool) {
	if s.schedulerReader == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "scheduler not configured")
		return nil, false
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		s.errorResponse(w, http.StatusBadRequest, "id is required")
		return nil, false
	}
	task, err := s.schedulerReader.GetTask(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.errorResponse(w, http.StatusNotFound, "schedule not found")
			return nil, false
		}
		s.logger.Warn("schedule lookup failed", "id", id, "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "query failed")
		return nil, false
	}
	if task == nil {
		s.errorResponse(w, http.StatusNotFound, "schedule not found")
		return nil, false
	}
	return task, true
}
