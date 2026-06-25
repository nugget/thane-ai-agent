package prompts

import (
	"strings"
	"testing"
)

func TestArchivistBaseTemplate(t *testing.T) {
	got := ArchivistBaseTemplate

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
			t.Errorf("base template missing expected phrase %q", phrase)
		}
	}
	if strings.Contains(got, "Supervisor Review") {
		t.Error("base template should not include the supervisor section")
	}
}

// TestArchivistBaseTemplate_SingleDrainMode locks in the post-#1024 shape:
// one "drain your queue" mode, no event-wake branches, and the explicit
// hand-off that session metadata belongs to the summarizer (so a future
// edit can't silently reintroduce the curator's false metadata claim or
// the two-wake-modes amplification).
func TestArchivistBaseTemplate_SingleDrainMode(t *testing.T) {
	normalized := strings.Join(strings.Fields(ArchivistBaseTemplate), " ")

	for _, gone := range []string{"Two ways you wake up", "Self-paced wake", "sole writer"} {
		if strings.Contains(normalized, gone) {
			t.Errorf("base template should no longer contain %q (single drain-queue mode)", gone)
		}
	}
	for _, phrase := range []string{
		"Pull your queue",
		"Ack every item you handle",
		"summarizer owns",
		"do NOT spawn loops",
	} {
		if !strings.Contains(normalized, phrase) {
			t.Errorf("base template missing drain-mode phrase %q", phrase)
		}
	}
}

func TestArchivistSupervisorInstructions(t *testing.T) {
	got := ArchivistSupervisorInstructions

	if !strings.Contains(got, "Supervisor Review") {
		t.Error("supervisor instructions should include the supervisor review section")
	}
	for _, phrase := range []string{
		"Evidence discipline",
		"Queue health",
		"Cadence calibration",
	} {
		if !strings.Contains(got, phrase) {
			t.Errorf("supervisor instructions missing expected phrase %q", phrase)
		}
	}
	if got != strings.TrimSpace(got) {
		t.Error("supervisor instructions must not have leading/trailing whitespace")
	}
}
