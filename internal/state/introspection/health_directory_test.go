package introspection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// directorySource behaves like a real directory audit: it keeps at most
// limit lines and counts them all.
func directorySource(lines []string, err error) func(context.Context, int) ([]string, int, error) {
	return func(_ context.Context, limit int) ([]string, int, error) {
		return lines[:min(len(lines), limit)], len(lines), err
	}
}

func forkLines(n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf(`name "p%d" shared by P%d Household (household, id-%d) and P%d (known, id-%d)`, i, i, i, i, i))
	}
	return out
}

// TestHealthContactDirectoryRow_Forks pins the fork part of the
// contact_directory lamp: it stands without the automated-address
// source, degrades the row with a bounded list and the remedy, reports a
// failed audit, and shares the row's detail with the automated part.
func TestHealthContactDirectoryRow_Forks(t *testing.T) {
	automatedLine := []string{"Forge Notices (admin, noreply@forge.example: no-reply)"}
	tests := []struct {
		name              string
		automated, forks  func(context.Context, int) ([]string, int, error)
		wantRow           bool
		wantStatus        string
		wantDetail        []string
		notDetail         []string
		wantSummaryNaming bool
	}{
		{
			name: "forks alone degrade the row with the remedy", forks: directorySource(forkLines(2), nil),
			wantRow: true, wantStatus: HealthDegraded, wantSummaryNaming: true,
			wantDetail: []string{"2 duplicate, shared-name or placeholder contact findings", `name "p1"`, `name "p2"`,
				"merge the duplicate into the record that should keep the name", "rename one", "move it to the record that owns it",
				"replace it with the real address", "CardDAV", "/v1/contacts", "Thane can forget a known duplicate", "operator's own message",
				"renames nothing and removes no address"},
			notDetail: []string{"automated-looking", "more)"},
		},
		{
			name: "one fork reads singular", forks: directorySource(forkLines(1), nil),
			wantRow: true, wantStatus: HealthDegraded, wantSummaryNaming: true,
			wantDetail: []string{"1 duplicate, shared-name or placeholder contact finding can"},
		},
		{
			name: "seven forks name five and count the rest", forks: directorySource(forkLines(7), nil),
			wantRow: true, wantStatus: HealthDegraded, wantSummaryNaming: true,
			wantDetail: []string{"7 duplicate, shared-name or placeholder contact findings", `name "p5"`, "(+2 more)"},
			notDetail:  []string{`name "p6"`, `name "p7"`},
		},
		{
			name: "a failed fork audit degrades with the error text", forks: directorySource(nil, errors.New("database is locked")),
			wantRow: true, wantStatus: HealthDegraded, wantSummaryNaming: true,
			wantDetail: []string{"contact fork audit failed: database is locked"},
		},
		{
			name: "a clean automated audit does not hide forks", automated: directorySource(nil, nil), forks: directorySource(forkLines(1), nil),
			wantRow: true, wantStatus: HealthDegraded, wantSummaryNaming: true,
			wantDetail: []string{`name "p1"`}, notDetail: []string{"automated-looking"},
		},
		{
			name: "both audits share the detail", automated: directorySource(automatedLine, nil), forks: directorySource(forkLines(1), nil),
			wantRow: true, wantStatus: HealthDegraded, wantSummaryNaming: true,
			wantDetail: []string{"1 automated-looking email address,", automatedLine[0], "1 duplicate, shared-name or placeholder contact finding", `name "p1"`},
		},
		{
			name: "two clean audits are ok", automated: directorySource(nil, nil), forks: directorySource(nil, nil),
			wantRow: true, wantStatus: HealthOK,
		},
		{name: "neither source wired contributes no row", wantRow: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := NewInspector(HealthSources{DirectoryFindings: tt.automated, DirectoryForks: tt.forks}).Health(context.Background())
			var row *HealthRow
			for i := range snap.Annunciator {
				if snap.Annunciator[i].Name == "contact_directory" {
					row = &snap.Annunciator[i]
				}
			}
			if (row != nil) != tt.wantRow {
				t.Fatalf("contact_directory row present = %v, want %v: %+v", row != nil, tt.wantRow, snap.Annunciator)
			}
			if row == nil {
				return
			}
			if row.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q (detail %q)", row.Status, tt.wantStatus, row.Detail)
			}
			if tt.wantStatus == HealthOK && row.Detail != "" {
				t.Errorf("a clean row carries detail %q", row.Detail)
			}
			for _, want := range tt.wantDetail {
				if !strings.Contains(row.Detail, want) {
					t.Errorf("detail = %q, want it to contain %q", row.Detail, want)
				}
			}
			for _, not := range tt.notDetail {
				if strings.Contains(row.Detail, not) {
					t.Errorf("detail = %q, must not contain %q", row.Detail, not)
				}
			}
			summary, _ := snapshotPayload(snap)["summary"].(string)
			if strings.Contains(summary, "contact_directory") != tt.wantSummaryNaming {
				t.Errorf("summary = %q, want contact_directory named=%v", summary, tt.wantSummaryNaming)
			}
		})
	}
}

// TestHealthContactDirectoryRow_ForksBounded pins the fork part's bounds
// against a source that does not clip: five lines at most, each cut on a
// rune boundary at the fork backstop.
func TestHealthContactDirectoryRow_ForksBounded(t *testing.T) {
	var gotLimit int
	huge := strings.Repeat("é€", 100*1024/5)
	src := func(_ context.Context, limit int) ([]string, int, error) {
		gotLimit = limit
		lines := make([]string, 0, limit)
		for i := range limit {
			lines = append(lines, fmt.Sprintf(`name "%s%d" shared by records`, huge, i))
		}
		return lines, 9, nil
	}
	snap := NewInspector(HealthSources{DirectoryForks: src}).Health(context.Background())
	var detail string
	for _, row := range snap.Annunciator {
		if row.Name == "contact_directory" {
			detail = row.Detail
		}
	}
	if gotLimit != maxDirectoryFindingsShown {
		t.Errorf("source limit = %d, want %d", gotLimit, maxDirectoryFindingsShown)
	}
	if !strings.Contains(detail, "9 duplicate, shared-name or placeholder contact findings") || !strings.Contains(detail, "(+4 more)") {
		t.Errorf("detail = %.300q, want the total of 9 and (+4 more)", detail)
	}
	if !utf8.ValidString(detail) {
		t.Error("detail is not valid UTF-8")
	}
	if limit := maxDirectoryFindingsShown*(maxDirectoryForkBytes+2) + 1024; len(detail) > limit {
		t.Errorf("detail is %d bytes, want at most %d", len(detail), limit)
	}
	if got := strings.Count(detail, "…"); got != maxDirectoryFindingsShown {
		t.Errorf("detail marks %d clipped findings, want %d", got, maxDirectoryFindingsShown)
	}
}
