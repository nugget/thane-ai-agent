package loop

import (
	"testing"
	"time"
)

func TestBuildDefinitionWarnings_DefaultCadenceAndDelegation(t *testing.T) {
	spec := Spec{
		Name:      "comal_burn_ban_monitor",
		Task:      "Check the county burn ban source hourly and update Home Assistant.",
		Operation: OperationService,
		Tags:      []string{"web", "ha"},
	}

	warnings := BuildDefinitionWarnings(spec)
	if len(warnings) < 3 {
		t.Fatalf("warnings = %#v, want at least 3 warnings", warnings)
	}
	codes := make(map[string]bool, len(warnings))
	for _, warning := range warnings {
		codes[warning.Code] = true
	}
	for _, code := range []string{
		"service_default_cadence",
		"task_mentions_cadence_without_explicit_sleep",
		"service_delegation_gating_enabled",
	} {
		if !codes[code] {
			t.Fatalf("warning codes = %#v, want %q", codes, code)
		}
	}
}

func TestBuildDefinitionWarnings_FixedCadenceJitterAndCompletion(t *testing.T) {
	spec := Spec{
		Name:         "battery_watch",
		Task:         "Watch batteries.",
		Operation:    OperationService,
		Completion:   CompletionConversation,
		SleepMin:     15 * time.Minute,
		SleepMax:     15 * time.Minute,
		SleepDefault: 15 * time.Minute,
	}

	warnings := BuildDefinitionWarnings(spec)
	codes := make(map[string]bool, len(warnings))
	for _, warning := range warnings {
		codes[warning.Code] = true
	}
	if !codes["service_completion_not_periodic"] {
		t.Fatalf("warning codes = %#v, want service_completion_not_periodic", codes)
	}
	if !codes["fixed_cadence_with_jitter"] {
		t.Fatalf("warning codes = %#v, want fixed_cadence_with_jitter", codes)
	}
}
