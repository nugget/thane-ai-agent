package delegate

import (
	"strings"
	"testing"
	"time"
)

func TestFormatExecSummary_CleanExecution(t *testing.T) {
	r := &Result{
		Iterations: 3,
		Duration:   8200 * time.Millisecond,
		ToolCalls: []ToolCallOutcome{
			{Name: "find_entity", Success: true},
			{Name: "call_service", Success: true},
			{Name: "get_state", Success: true},
		},
	}

	got := formatExecSummary(r)

	checks := []string{
		"--- execution summary ---",
		"iterations: 3",
		"duration: 8.2s",
		"tool_calls: find_entity(ok) → call_service(ok) → get_state(ok)",
		"errors: 0",
	}
	for _, want := range checks {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestFormatExecSummary_WithErrors(t *testing.T) {
	r := &Result{
		Iterations: 5,
		Duration:   15400 * time.Millisecond,
		ToolCalls: []ToolCallOutcome{
			{Name: "get_state", Success: true},
			{Name: "call_service", Success: false},
			{Name: "get_state", Success: true},
			{Name: "call_service", Success: false},
			{Name: "call_service", Success: true},
		},
	}

	got := formatExecSummary(r)

	checks := []string{
		"iterations: 5",
		"duration: 15.4s",
		"call_service(err)",
		"call_service(ok)",
		"errors: 2",
	}
	for _, want := range checks {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestFormatExecSummary_NoToolCalls(t *testing.T) {
	r := &Result{
		Iterations: 1,
		Duration:   500 * time.Millisecond,
		ToolCalls:  nil,
	}

	got := formatExecSummary(r)

	checks := []string{
		"iterations: 1",
		"duration: 500ms",
		"tool_calls: (none)",
		"errors: 0",
	}
	for _, want := range checks {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"sub-second", 450 * time.Millisecond, "450ms"},
		{"seconds", 8200 * time.Millisecond, "8.2s"},
		{"minutes", 72 * time.Second, "1m12s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDuration(tt.d)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}
