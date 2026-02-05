// Package scheduler handles future task scheduling and execution.
package scheduler

import (
	"time"
)

// Task is the definition of a scheduled action.
type Task struct {
	ID        string    `json:"id"`       // UUIDv7
	Name      string    `json:"name"`     // Human-readable label
	Schedule  Schedule  `json:"schedule"` // When to run
	Payload   Payload   `json:"payload"`  // What to do
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"` // Session or user ID
	UpdatedAt time.Time `json:"updated_at"`
}

// Schedule defines when a task should run.
type Schedule struct {
	Kind     ScheduleKind `json:"kind"`
	At       *time.Time   `json:"at,omitempty"`       // For "at" kind
	Every    *Duration    `json:"every,omitempty"`    // For "every" kind
	Cron     string       `json:"cron,omitempty"`     // For "cron" kind
	Timezone string       `json:"timezone,omitempty"` // IANA timezone
}

// ScheduleKind identifies the schedule type.
type ScheduleKind string

const (
	ScheduleAt    ScheduleKind = "at"    // One-shot at specific time
	ScheduleEvery ScheduleKind = "every" // Recurring interval
	ScheduleCron  ScheduleKind = "cron"  // Cron expression
)

// Duration wraps time.Duration for JSON serialization.
type Duration struct {
	time.Duration
}

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.String() + `"`), nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	// Remove quotes
	s := string(b)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}

// Payload defines what action to take when a task fires.
type Payload struct {
	Kind   PayloadKind    `json:"kind"`
	Target string         `json:"target,omitempty"` // Session ID, entity ID, etc.
	Data   map[string]any `json:"data,omitempty"`   // Kind-specific data
}

// PayloadKind identifies the payload type.
type PayloadKind string

const (
	PayloadWake       PayloadKind = "wake"       // Wake the agent with a message
	PayloadService    PayloadKind = "service"    // Call an HA service
	PayloadAutomation PayloadKind = "automation" // Trigger an HA automation
	PayloadWebhook    PayloadKind = "webhook"    // Call external webhook
)

// Execution represents a single run of a task.
type Execution struct {
	ID          string          `json:"id"`           // UUIDv7
	TaskID      string          `json:"task_id"`      // FK to Task
	ScheduledAt time.Time       `json:"scheduled_at"` // When it was supposed to run
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Status      ExecutionStatus `json:"status"`
	Result      string          `json:"result,omitempty"` // Output or error
}

// ExecutionStatus indicates the state of an execution.
type ExecutionStatus string

const (
	StatusPending   ExecutionStatus = "pending"
	StatusRunning   ExecutionStatus = "running"
	StatusCompleted ExecutionStatus = "completed"
	StatusFailed    ExecutionStatus = "failed"
	StatusSkipped   ExecutionStatus = "skipped" // Missed window, chose not to catch up
)

// NextRun calculates the next execution time for a task.
func (t *Task) NextRun(after time.Time) (time.Time, bool) {
	switch t.Schedule.Kind {
	case ScheduleAt:
		if t.Schedule.At != nil && t.Schedule.At.After(after) {
			return *t.Schedule.At, true
		}
		return time.Time{}, false // One-shot already passed

	case ScheduleEvery:
		if t.Schedule.Every == nil {
			return time.Time{}, false
		}
		// Find next interval after 'after'
		interval := t.Schedule.Every.Duration
		base := t.CreatedAt
		if base.IsZero() {
			base = after
		}
		// Calculate how many intervals have passed
		elapsed := after.Sub(base)
		if elapsed < 0 {
			return base, true
		}
		intervals := int64(elapsed/interval) + 1
		next := base.Add(time.Duration(intervals) * interval)
		return next, true

	case ScheduleCron:
		// TODO: Implement cron parsing
		// For now, return false
		return time.Time{}, false

	default:
		return time.Time{}, false
	}
}
