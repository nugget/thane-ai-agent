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
	if strings.Contains(got, "Use ISO 8601 timestamps") {
		t.Error("prompt should not ask ego loop to persist raw timestamps by default")
	}
	if !strings.Contains(got, "how recently ego.md was updated") {
		t.Error("prompt should point to generated freshness context")
	}
}

func TestEgoPrompt_SupervisorTurn(t *testing.T) {
	got := EgoPrompt(true)

	if !strings.Contains(got, "Supervisor Review") {
		t.Error("supervisor turn should include supervisor review section")
	}
	if !strings.Contains(got, "Ego loop iteration") {
		t.Error("supervisor turn should still include base prompt")
	}
}
