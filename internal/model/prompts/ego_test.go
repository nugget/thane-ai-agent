package prompts

import (
	"strings"
	"testing"
)

func TestEgoPrompt_NormalIteration(t *testing.T) {
	got := EgoPrompt(false)

	for _, phrase := range []string{
		"Ego loop iteration",
		"replace_output_ego_state",
		"set_next_sleep",
		"What ego.md Is For",
		"What ego.md Is NOT For",
		"durable output contract",
	} {
		if !strings.Contains(got, phrase) {
			t.Errorf("prompt missing expected phrase %q", phrase)
		}
	}
	if strings.Contains(got, "Supervisor Review") {
		t.Error("normal iteration should not include supervisor section")
	}
}

func TestEgoPrompt_SupervisorIteration(t *testing.T) {
	got := EgoPrompt(true)

	if !strings.Contains(got, "Supervisor Review") {
		t.Error("supervisor iteration should include supervisor review section")
	}
	if !strings.Contains(got, "Ego loop iteration") {
		t.Error("supervisor iteration should still include base prompt")
	}
}
