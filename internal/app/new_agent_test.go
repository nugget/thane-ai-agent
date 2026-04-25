package app

import (
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/scheduler"
)

func TestSyncPeriodicReflectionTaskUpdatesDriftedTask(t *testing.T) {
	interval := 24 * time.Hour
	desired := scheduler.Payload{
		Kind: scheduler.PayloadWake,
		Data: map[string]any{
			"message":       "periodic_reflection",
			"model":         "claude-sonnet-4-20250514",
			"local_only":    "false",
			"quality_floor": "7",
		},
	}

	task := &scheduler.Task{
		Name: periodicReflectionTaskName,
		Schedule: scheduler.Schedule{
			Kind:  scheduler.ScheduleEvery,
			Every: &scheduler.Duration{Duration: 15 * time.Minute},
		},
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWake,
			Data: map[string]any{
				"message":       "periodic_reflection",
				"model":         "old-model",
				"local_only":    "true",
				"quality_floor": "3",
				"extra":         "stale",
			},
		},
		Enabled: false,
	}

	if !syncPeriodicReflectionTask(task, interval, desired) {
		t.Fatal("syncPeriodicReflectionTask() = false, want true")
	}
	if task.Schedule.Kind != scheduler.ScheduleEvery {
		t.Fatalf("Schedule.Kind = %q, want %q", task.Schedule.Kind, scheduler.ScheduleEvery)
	}
	if task.Schedule.Every == nil || task.Schedule.Every.Duration != interval {
		t.Fatalf("Schedule.Every = %v, want %v", task.Schedule.Every, interval)
	}
	if !task.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if !periodicReflectionPayloadEqual(task.Payload, desired) {
		t.Fatalf("Payload = %#v, want %#v", task.Payload, desired)
	}
}

func TestSyncPeriodicReflectionTaskNoopWhenAlreadyDesired(t *testing.T) {
	interval := 24 * time.Hour
	task := &scheduler.Task{
		Name: periodicReflectionTaskName,
		Schedule: scheduler.Schedule{
			Kind:  scheduler.ScheduleEvery,
			Every: &scheduler.Duration{Duration: interval},
		},
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWake,
			Data: map[string]any{
				"message":       "periodic_reflection",
				"model":         "claude-sonnet-4-20250514",
				"local_only":    "false",
				"quality_floor": "7",
			},
		},
		Enabled: true,
	}

	if syncPeriodicReflectionTask(task, interval, cloneSchedulerPayload(task.Payload)) {
		t.Fatal("syncPeriodicReflectionTask() = true, want false")
	}
}

func TestPeriodicReflectionPayloadEqualRejectsExtraFields(t *testing.T) {
	base := scheduler.Payload{
		Kind: scheduler.PayloadWake,
		Data: map[string]any{
			"message":       "periodic_reflection",
			"model":         "claude-sonnet-4-20250514",
			"local_only":    "false",
			"quality_floor": "7",
		},
	}
	withExtra := cloneSchedulerPayload(base)
	withExtra.Data["extra"] = "stale"

	if periodicReflectionPayloadEqual(base, withExtra) {
		t.Fatal("periodicReflectionPayloadEqual() = true, want false")
	}
}
