package email

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// TestDraftGetHistoryKeepsTheFirstVersion pins email_draft_get's
// history: at most ten versions, oldest first, the first always the
// draft as it was written, and history_omitted counting the versions
// left out between it and the latest.
func TestDraftGetHistoryKeepsTheFirstVersion(t *testing.T) {
	notes := func(ids ...int) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			out = append(out, fmt.Sprintf("v%d", id))
		}
		return out
	}
	tests := []struct {
		name        string
		versions    int
		wantOmitted int
		wantNotes   []string
	}{
		{"a short history shows every version", 3, 0, notes(0, 1, 2)},
		{"ten versions show in full", 10, 0, notes(0, 1, 2, 3, 4, 5, 6, 7, 8, 9)},
		{"a long history keeps the first and the latest nine", 12, 2, notes(0, 3, 4, 5, 6, 7, 8, 9, 10, 11)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			var e draftEntry
			for i := range tt.versions {
				e.History = append(e.History, draftRevision{By: "email-draft-review", At: now.Add(time.Duration(i-tt.versions) * time.Minute), Note: fmt.Sprintf("v%d", i)})
			}
			before := slices.Clone(e.History)
			header := newDraftGetHeader(e, AccountConfig{}, nil, now)
			got := make([]string, 0, len(header.History))
			for _, r := range header.History {
				got = append(got, r.Note)
			}
			if !slices.Equal(got, tt.wantNotes) || header.HistoryOmitted != tt.wantOmitted {
				t.Errorf("history = %v omitted %d, want %v omitted %d", got, header.HistoryOmitted, tt.wantNotes, tt.wantOmitted)
			}
			if !slices.Equal(e.History, before) {
				t.Errorf("rendering the header changed the entry's history: %v", e.History)
			}
		})
	}
}
