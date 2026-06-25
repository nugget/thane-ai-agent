package prompts

import (
	"strings"
	"testing"
)

func TestEgoBaseTemplate(t *testing.T) {
	got := EgoBaseTemplate

	for _, phrase := range []string{
		"Ego loop iteration",
		"replace_output_ego_state",
		"set_next_sleep",
		"What ego.md Is For",
		"What ego.md Is NOT For",
		"durable output contract",
	} {
		if !strings.Contains(got, phrase) {
			t.Errorf("base template missing expected phrase %q", phrase)
		}
	}
	if strings.Contains(got, "Supervisor Review") {
		t.Error("base template should not include the supervisor section")
	}
	if strings.Contains(got, "Use ISO 8601 timestamps") {
		t.Error("base template should not ask ego loop to persist raw timestamps by default")
	}
	if !strings.Contains(got, "how recently ego.md was updated") {
		t.Error("base template should point to generated freshness context")
	}
}

func TestEgoSupervisorInstructions(t *testing.T) {
	got := EgoSupervisorInstructions

	if !strings.Contains(got, "Supervisor Review") {
		t.Error("supervisor instructions should include the supervisor review section")
	}
	// Applied as a prepended prompt prefix via SupervisorProfile.Instructions,
	// so unlike the old appended augmentation it must carry no surrounding
	// blank lines.
	if got != strings.TrimSpace(got) {
		t.Error("supervisor instructions must not have leading/trailing whitespace")
	}
}
