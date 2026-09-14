package tools

import (
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestLoopStatusHealthUnpublishedDossierTargets: loop_status names each
// unpublished dossier's contact, capped at three with the rest counted.
// The introspection census pins the same literal lines, so the two
// health rollups are held to one wording.
func TestLoopStatusHealthUnpublishedDossierTargets(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	w := func(target string, failures int, ago time.Duration) looppkg.UnpublishedWrite {
		return looppkg.UnpublishedWrite{Tool: "contact_dossier_write", Target: target, Failures: failures, At: now.Add(-ago)}
	}
	cases := []struct {
		name   string
		writes []looppkg.UnpublishedWrite
		want   string
	}{
		{
			name:   "one contact",
			writes: []looppkg.UnpublishedWrite{w("8c1d2e3f-4a5b-4c6d-8e7f-9a0b1c2d3e4f", 7, 2*time.Hour)},
			want: "contact_curator (wake at -2h ended without publishing contact_dossier_write for contact " +
				"8c1d2e3f-4a5b-4c6d-8e7f-9a0b1c2d3e4f after 7 rejections)",
		},
		{
			name: "capped at three",
			writes: []looppkg.UnpublishedWrite{
				w("b7e6d5c4-3b2a-4190-8f7e-6d5c4b3a2918", 1, 2*time.Hour),
				w("d4c3b2a1-0f9e-4d8c-b7a6-958473625140", 2, 3*time.Hour),
				w("8c1d2e3f-4a5b-4c6d-8e7f-9a0b1c2d3e4f", 7, time.Hour),
				w("3f2a1c9e-7b4d-4e8a-9c21-5d6e7f8a9b0c", 4, time.Hour),
			},
			want: "contact_curator (" +
				"wake at -1h ended without publishing contact_dossier_write for contact 3f2a1c9e-7b4d-4e8a-9c21-5d6e7f8a9b0c after 4 rejections; " +
				"wake at -1h ended without publishing contact_dossier_write for contact 8c1d2e3f-4a5b-4c6d-8e7f-9a0b1c2d3e4f after 7 rejections; " +
				"wake at -2h ended without publishing contact_dossier_write for contact b7e6d5c4-3b2a-4190-8f7e-6d5c4b3a2918 after 1 rejection (+1 more))",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := loopStatusHealth([]looppkg.Status{
				{Name: "contact_curator", State: looppkg.StateSleeping, UnpublishedWrites: tc.writes},
			}, now)
			if h["degraded"] != 1 {
				t.Errorf("degraded = %v, want 1", h["degraded"])
			}
			dl, _ := h["degraded_loops"].([]string)
			if len(dl) != 1 || dl[0] != tc.want {
				t.Errorf("degraded_loops = %q\nwant [%q]", dl, tc.want)
			}
		})
	}
}
