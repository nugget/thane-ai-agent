package loop

import (
	"testing"
	"time"
)

func TestScheduleConditionEvaluateInsideWindow(t *testing.T) {
	t.Parallel()

	conditions := Conditions{
		Schedule: &ScheduleCondition{
			Timezone: "America/Chicago",
			Windows: []ScheduleWindow{{
				Days:  []string{"mon"},
				Start: "09:00",
				End:   "17:00",
			}},
		},
	}

	now := time.Date(2026, 4, 6, 15, 0, 0, 0, time.UTC) // Monday 10:00 CDT
	status := conditions.Evaluate(now)
	if !status.Eligible {
		t.Fatalf("Eligible = false, want true (status=%+v)", status)
	}
	wantNext := time.Date(2026, 4, 6, 22, 0, 0, 0, time.UTC)
	if !status.NextTransitionAt.Equal(wantNext) {
		t.Fatalf("NextTransitionAt = %v, want %v", status.NextTransitionAt, wantNext)
	}
}

func TestScheduleConditionEvaluateWrapsMidnight(t *testing.T) {
	t.Parallel()

	conditions := Conditions{
		Schedule: &ScheduleCondition{
			Timezone: "America/Chicago",
			Windows: []ScheduleWindow{{
				Days:  []string{"fri"},
				Start: "22:00",
				End:   "06:00",
			}},
		},
	}

	now := time.Date(2026, 4, 11, 8, 0, 0, 0, time.UTC) // Saturday 03:00 CDT
	status := conditions.Evaluate(now)
	if !status.Eligible {
		t.Fatalf("Eligible = false, want true (status=%+v)", status)
	}
	wantNext := time.Date(2026, 4, 11, 11, 0, 0, 0, time.UTC)
	if !status.NextTransitionAt.Equal(wantNext) {
		t.Fatalf("NextTransitionAt = %v, want %v", status.NextTransitionAt, wantNext)
	}
}

func TestScheduleConditionValidateRejectsInvalidWindow(t *testing.T) {
	t.Parallel()

	conditions := Conditions{
		Schedule: &ScheduleCondition{
			Windows: []ScheduleWindow{{
				Days:  []string{"funday"},
				Start: "09:00",
				End:   "17:00",
			}},
		},
	}
	if err := conditions.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want invalid weekday error")
	}
}
