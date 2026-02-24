package web

import (
	"net/http"
	"time"

	"github.com/nugget/thane-ai-agent/internal/scheduler"
)

// TasksData is the template context for the tasks list page.
type TasksData struct {
	PageData
	Tasks []*taskRow
}

// taskRow is a display-friendly wrapper around a scheduled task.
type taskRow struct {
	ID             string
	Name           string
	ScheduleKind   string
	ScheduleDesc   string
	NextFire       string
	Enabled        bool
	LastExecStatus string
	CreatedBy      string
}

// TaskDetailData is the template context for a single task.
type TaskDetailData struct {
	PageData
	Task       *scheduler.Task
	NextFire   string
	Executions []*scheduler.Execution
}

// handleTasks renders the tasks list page.
func (s *WebServer) handleTasks(w http.ResponseWriter, r *http.Request) {
	if s.taskStore == nil {
		http.Error(w, "task store not configured", http.StatusServiceUnavailable)
		return
	}

	tasks, err := s.taskStore.ListTasks(false) // include disabled
	if err != nil {
		s.logger.Error("task list failed", "error", err)
		http.Error(w, "list failed", http.StatusInternalServerError)
		return
	}

	data := TasksData{
		PageData: PageData{
			BrandName: s.brandName,
			ActiveNav: "tasks",
		},
	}

	now := time.Now()
	for _, t := range tasks {
		row := &taskRow{
			ID:           t.ID,
			Name:         t.Name,
			ScheduleKind: string(t.Schedule.Kind),
			ScheduleDesc: describeSchedule(t.Schedule),
			Enabled:      t.Enabled,
			CreatedBy:    t.CreatedBy,
		}

		if next, ok := t.NextRun(now); ok {
			row.NextFire = timeAgo(now.Add(-time.Since(next))) // show relative
			if next.After(now) {
				d := next.Sub(now)
				row.NextFire = "in " + formatDuration(d)
			}
		} else {
			row.NextFire = "—"
		}

		// Fetch last execution for status badge.
		execs, err := s.taskStore.GetTaskExecutions(t.ID, 1)
		if err == nil && len(execs) > 0 {
			row.LastExecStatus = string(execs[0].Status)
		}

		data.Tasks = append(data.Tasks, row)
	}

	s.render(w, r, "tasks.html", data)
}

// handleTaskDetail renders the detail view for a single task.
func (s *WebServer) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	if s.taskStore == nil {
		http.Error(w, "task store not configured", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	task, err := s.taskStore.GetTask(id)
	if err != nil {
		s.logger.Error("task detail failed", "id", id, "error", err)
		http.Error(w, "load failed", http.StatusInternalServerError)
		return
	}
	if task == nil {
		http.NotFound(w, r)
		return
	}

	execs, err := s.taskStore.GetTaskExecutions(id, 20)
	if err != nil {
		s.logger.Error("task executions failed", "id", id, "error", err)
		execs = nil
	}

	var nextFire string
	if next, ok := task.NextRun(time.Now()); ok {
		nextFire = formatTime(next)
	} else {
		nextFire = "—"
	}

	data := TaskDetailData{
		PageData: PageData{
			BrandName: s.brandName,
			ActiveNav: "tasks",
		},
		Task:       task,
		NextFire:   nextFire,
		Executions: execs,
	}

	s.render(w, r, "task_detail.html", data)
}

// describeSchedule returns a human-readable description of a task schedule.
func describeSchedule(s scheduler.Schedule) string {
	switch s.Kind {
	case "at":
		if s.At != nil {
			return "once at " + formatTime(*s.At)
		}
		return "once (no time set)"
	case "every":
		if s.Every != nil {
			return "every " + s.Every.String()
		}
		return "recurring (no interval set)"
	case "cron":
		if s.Cron != "" {
			return "cron: " + s.Cron
		}
		return "cron (no expression)"
	default:
		return string(s.Kind)
	}
}
