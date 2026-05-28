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

// TestCuratorPrompt_TwoModesPresent verifies the prompt teaches the
// curator both wake modes — self-paced and event-triggered
// (session_close). Lock this in so a future prompt edit doesn't
// silently drop one path; the curator's behavior on each shape is
// load-bearing for the issue-989 wake delivery.
func TestCuratorPrompt_TwoModesPresent(t *testing.T) {
	got := CuratorPrompt(false)
	for _, phrase := range []string{
		"Two ways you wake up",
		"Session-close wake",
		"Self-paced wake",
		"session_close",
		"archive_session_transcript",
		// Reference to the runtime's notification block (the literal
		// header is "Loop notifications for this run:"). Normalize
		// out whitespace so prompt line-wrapping doesn't break this
		// assertion.
		"notifications for this run",
	} {
		normalized := strings.Join(strings.Fields(got), " ")
		if !strings.Contains(normalized, phrase) {
			t.Errorf("prompt missing two-modes phrase %q", phrase)
		}
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
