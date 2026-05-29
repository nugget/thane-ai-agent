package prompts

import (
	"strings"
	"testing"
)

func TestArchivistPrompt_NormalIteration(t *testing.T) {
	got := ArchivistPrompt(false)

	for _, phrase := range []string{
		"Archivist loop iteration",
		"queue_pull",
		"queue_ack",
		"queue_enqueue",
		"replace_output_archivist_state",
		"set_next_sleep",
		"dossier",
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

// TestArchivistPrompt_SingleDrainMode locks in the post-#1024 shape: one
// "drain your queue" mode, no event-wake branches, and the explicit
// hand-off that session metadata belongs to the summarizer (so a future
// edit can't silently reintroduce the curator's false metadata claim or
// the two-wake-modes amplification).
func TestArchivistPrompt_SingleDrainMode(t *testing.T) {
	got := ArchivistPrompt(false)
	normalized := strings.Join(strings.Fields(got), " ")

	for _, gone := range []string{"Two ways you wake up", "Self-paced wake", "sole writer"} {
		if strings.Contains(normalized, gone) {
			t.Errorf("prompt should no longer contain %q (single drain-queue mode)", gone)
		}
	}
	for _, phrase := range []string{
		"Pull your queue",
		"Ack every item you handle",
		"summarizer owns",
		"do NOT spawn loops",
	} {
		if !strings.Contains(normalized, phrase) {
			t.Errorf("prompt missing drain-mode phrase %q", phrase)
		}
	}
}

func TestArchivistPrompt_SupervisorTurn(t *testing.T) {
	got := ArchivistPrompt(true)

	if !strings.Contains(got, "Supervisor Review") {
		t.Error("supervisor turn should include supervisor review section")
	}
	if !strings.Contains(got, "Archivist loop iteration") {
		t.Error("supervisor turn should still include base prompt")
	}
	for _, phrase := range []string{
		"Evidence discipline",
		"Queue health",
		"Cadence calibration",
	} {
		if !strings.Contains(got, phrase) {
			t.Errorf("supervisor section missing expected phrase %q", phrase)
		}
	}
}
