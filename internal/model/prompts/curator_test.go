package prompts

import (
	"strings"
	"testing"
)

func TestCuratorPrompt_NormalIteration(t *testing.T) {
	got := CuratorPrompt(false)

	for _, phrase := range []string{
		"Curator loop iteration",
		"replace_output_curator_state",
		"set_next_sleep",
		"dossier",
		"durable output contract",
		"One subject per iteration",
		"evidence",
	} {
		if !strings.Contains(got, phrase) {
			t.Errorf("prompt missing expected phrase %q", phrase)
		}
	}
	if strings.Contains(got, "Supervisor Review") {
		t.Error("normal iteration should not include supervisor section")
	}
}

func TestCuratorPrompt_SupervisorTurn(t *testing.T) {
	got := CuratorPrompt(true)

	if !strings.Contains(got, "Supervisor Review") {
		t.Error("supervisor turn should include supervisor review section")
	}
	if !strings.Contains(got, "Curator loop iteration") {
		t.Error("supervisor turn should still include base prompt")
	}
	for _, phrase := range []string{
		"Evidence discipline",
		"Subject selection",
		"Cadence calibration",
	} {
		if !strings.Contains(got, phrase) {
			t.Errorf("supervisor section missing expected phrase %q", phrase)
		}
	}
}
