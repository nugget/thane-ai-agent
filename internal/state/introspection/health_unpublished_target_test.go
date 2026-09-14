package introspection

import (
	"fmt"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// dossierTargetLines are the exact lines loop_status's health rollup
// emits for a loop whose unpublished writes are dossiers; the tools
// package pins the same literals, so the two rollups are held to one
// wording.
var dossierTargetLines = map[string]string{
	"one contact": "contact_curator (wake at -2h ended without publishing contact_dossier_write for contact " +
		"8c1d2e3f-4a5b-4c6d-8e7f-9a0b1c2d3e4f after 7 rejections)",
	"capped at three": "contact_curator (" +
		"wake at -1h ended without publishing contact_dossier_write for contact 3f2a1c9e-7b4d-4e8a-9c21-5d6e7f8a9b0c after 4 rejections; " +
		"wake at -1h ended without publishing contact_dossier_write for contact 8c1d2e3f-4a5b-4c6d-8e7f-9a0b1c2d3e4f after 7 rejections; " +
		"wake at -2h ended without publishing contact_dossier_write for contact b7e6d5c4-3b2a-4190-8f7e-6d5c4b3a2918 after 1 rejection (+1 more))",
}

// dossierTargetWrites are the unpublished writes behind each line.
func dossierTargetWrites(now time.Time) map[string][]looppkg.UnpublishedWrite {
	w := func(target string, failures int, ago time.Duration) looppkg.UnpublishedWrite {
		return looppkg.UnpublishedWrite{Tool: "contact_dossier_write", Target: target, Failures: failures, At: now.Add(-ago)}
	}
	return map[string][]looppkg.UnpublishedWrite{
		"one contact": {w("8c1d2e3f-4a5b-4c6d-8e7f-9a0b1c2d3e4f", 7, 2*time.Hour)},
		"capped at three": {
			w("b7e6d5c4-3b2a-4190-8f7e-6d5c4b3a2918", 1, 2*time.Hour),
			w("d4c3b2a1-0f9e-4d8c-b7a6-958473625140", 2, 3*time.Hour),
			w("8c1d2e3f-4a5b-4c6d-8e7f-9a0b1c2d3e4f", 7, time.Hour),
			w("3f2a1c9e-7b4d-4e8a-9c21-5d6e7f8a9b0c", 4, time.Hour),
		},
	}
}

// TestLoopCensusUnpublishedDossierTargets: the census names each
// unpublished dossier's contact exactly as loop_status's rollup does,
// capped at three with the rest counted.
func TestLoopCensusUnpublishedDossierTargets(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	writes := dossierTargetWrites(now)
	for name, want := range dossierTargetLines {
		t.Run(name, func(t *testing.T) {
			status := looppkg.Status{Name: "contact_curator", State: looppkg.StateSleeping, UnpublishedWrites: writes[name]}
			census := buildLoopCensus([]looppkg.Status{status}, now)

			if census.Degraded != 1 {
				t.Errorf("degraded = %d, want 1", census.Degraded)
			}
			if len(census.UnpublishedWrites) != 1 || census.UnpublishedWrites[0] != want {
				t.Fatalf("unpublished_writes = %q\nwant [%q]", census.UnpublishedWrites, want)
			}
			if lockstep := fmt.Sprintf("%s (%s)", status.Name, looppkg.DegradedReason(status, now)); lockstep != want {
				t.Errorf("loop_status line %q diverges from census line %q", lockstep, want)
			}
		})
	}
}
