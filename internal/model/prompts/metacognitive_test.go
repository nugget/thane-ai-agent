package prompts

import (
	"strings"
	"testing"
)

func TestMetacognitiveBaseTemplate(t *testing.T) {
	got := MetacognitiveBaseTemplate

	for _, phrase := range []string{
		"Metacognitive loop iteration",
		"Declared Durable",
		"replace_output_metacognitive_state",
		"set_next_sleep",
		"exactly two special tools",
		"File tools, exec, and session management",
		"sanctioned interface",
		"how recently metacognitive.md was",
		"Do not copy raw sensor timestamps",
	} {
		if !strings.Contains(got, phrase) {
			t.Errorf("base template missing expected phrase %q", phrase)
		}
	}
	for _, unwanted := range []string{
		"Supervisor Review",         // supervisor content lives in the instructions const
		"file_write",                // file tools are excluded
		"append_ego_observation",    // removed in #575
		"Read it carefully",         // ambiguous phrasing, removed
		"absolute format (RFC3339",  // old timestamp guidance, removed
		"Deltas become meaningless", // old timestamp guidance, removed
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("base template should not contain %q", unwanted)
		}
	}
}

func TestMetacognitiveSupervisorInstructions(t *testing.T) {
	got := MetacognitiveSupervisorInstructions

	for _, phrase := range []string{
		"Supervisor Review",
		"Blind spots",
		"Drift detection",
	} {
		if !strings.Contains(got, phrase) {
			t.Errorf("supervisor instructions missing expected phrase %q", phrase)
		}
	}
	if strings.Contains(got, "append_ego_observation") {
		t.Error("supervisor instructions should not reference removed append_ego_observation tool")
	}
	// Applied as a prepended prompt prefix via SupervisorProfile.Instructions,
	// so it must carry no surrounding blank lines.
	if got != strings.TrimSpace(got) {
		t.Error("supervisor instructions must not have leading/trailing whitespace")
	}
}
