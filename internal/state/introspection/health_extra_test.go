package introspection

import (
	"context"
	"testing"
	"time"
)

// TestVersionInfoMarksReTagBoundary pins the re-tag disclosure: when the
// previous version's label points at the commit the running build was
// made from, the deploy story says so — the first production re-tag
// (v0.10.2-400 → v0.10.3, same commit) read as an upgrade that wasn't
// one, and the loop reading it carried the retired label forward.
func TestVersionInfoMarksReTagBoundary(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		previous BootRecord
		want     bool
	}{
		{"re-tag of the same commit", BootRecord{At: now.Add(-2 * time.Hour), Version: "v0.10.2-400-g77108987", Commit: "7710898761a2"}, true},
		{"real upgrade", BootRecord{At: now.Add(-2 * time.Hour), Version: "v0.10.2", Commit: "0123456789ab"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			boots := []BootRecord{
				{At: now.Add(-30 * time.Minute), Version: "v0.10.3", Commit: "77108987"},
				tt.previous,
			}
			insp := NewInspector(HealthSources{
				BuildVersion: "v0.10.3",
				BuildCommit:  "77108987",
				BootHistory:  func(context.Context) ([]BootRecord, error) { return boots, nil },
			})
			insp.now = func() time.Time { return now }
			v := insp.Health(context.Background()).Version
			if v.PreviousSameCommit != tt.want {
				t.Errorf("previous_same_commit = %v, want %v (previous %s@%s vs running @77108987)",
					v.PreviousSameCommit, tt.want, tt.previous.Version, tt.previous.Commit)
			}
		})
	}
}
