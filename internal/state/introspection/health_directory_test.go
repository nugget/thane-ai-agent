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

func automatedLines(n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("Record %d (admin, noreply@r%d.example: no-reply)", i, i))
	}
	return out
}

// recordLimit wraps a directory source so a test can see the limit the
// row gave it; got stays -1 until the source is called.
func recordLimit(src func(context.Context, int) ([]string, int, error), got *int) func(context.Context, int) ([]string, int, error) {
	*got = -1
	return func(ctx context.Context, limit int) ([]string, int, error) {
		*got = limit
		return src(ctx, limit)
	}
}

// ignoreLimit is a directory source that returns every line whatever
// limit it is given.
func ignoreLimit(lines []string) func(context.Context, int) ([]string, int, error) {
	return func(context.Context, int) ([]string, int, error) {
		return lines, len(lines), nil
	}
}

func contactDirectoryDetail(t *testing.T, src HealthSources) string {
	t.Helper()
	for _, row := range NewInspector(src).Health(context.Background()).Annunciator {
		if row.Name == "contact_directory" {
			return row.Detail
		}
	}
	t.Fatal("no contact_directory row")
	return ""
}

// TestHealthContactDirectoryRow_SharedBudget pins the row's one budget
// of five named findings across both audits: fork findings are named
// first, in the part rendered first, and the automated-address audit is
// asked for what they leave, down to none, while its total still
// appears; a fork audit that finds nothing or fails leaves the whole
// budget.
func TestHealthContactDirectoryRow_SharedBudget(t *testing.T) {
	tests := []struct {
		name                         string
		forks                        func(context.Context, int) ([]string, int, error)
		automated                    int
		wantForkLimit, wantAutoLimit int
		forkPart, automatedPart      []string
		notDetail                    []string
	}{
		{
			name: "four forks leave one name for three automated addresses", forks: directorySource(forkLines(4), nil), automated: 3,
			wantForkLimit: 5, wantAutoLimit: 1,
			forkPart:      []string{"4 duplicate, shared-name or placeholder contact findings", `name "p1"`, `name "p4"`},
			automatedPart: []string{"3 automated-looking email addresses", "Record 1 (admin, noreply@r1.example: no-reply) (+2 more)."},
			notDetail:     []string{"Record 2 (", "none is named"},
		},
		{
			name: "seven forks leave no name for two automated addresses", forks: directorySource(forkLines(7), nil), automated: 2,
			wantForkLimit: 5, wantAutoLimit: 0,
			forkPart: []string{"7 duplicate, shared-name or placeholder contact findings", `name "p5"`, "(+2 more)"},
			automatedPart: []string{
				"hold 2 automated-looking email addresses, which the runtime reads at known whatever the record's zone; none is named here, since the contact_directory row names at most 5 findings in all and names duplicate, shared-name or placeholder findings first.",
				"The operator should demote each record to known",
			},
			notDetail: []string{`name "p6"`, "Record 1 (", ": (+"},
		},
		{
			name: "five forks leave no name for the one automated address", forks: directorySource(forkLines(5), nil), automated: 1,
			wantForkLimit: 5, wantAutoLimit: 0,
			forkPart:      []string{`name "p5"`, "removes no address. "},
			automatedPart: []string{"Contact records above known hold 1 automated-looking email address, which the runtime reads at known whatever the record's zone; it is not named here, since the contact_directory row names at most 5 findings in all"},
			notDetail:     []string{"none is named", "Record 1 ("},
		},
		{
			name: "a clean fork audit leaves the whole budget", forks: directorySource(nil, nil), automated: 7,
			wantForkLimit: 5, wantAutoLimit: 5,
			automatedPart: []string{"Record 5 (", "(+2 more)"},
			notDetail:     []string{"Record 6 (", "duplicate"},
		},
		{
			name: "a failed fork audit leaves the whole budget", forks: directorySource(nil, errors.New("database is locked")), automated: 7,
			wantForkLimit: 5, wantAutoLimit: 5,
			forkPart:      []string{"The contact fork audit failed: database is locked. "},
			automatedPart: []string{"Record 5 (", "(+2 more)"},
			notDetail:     []string{"Record 6 ("},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var forkLimit, autoLimit int
			detail := contactDirectoryDetail(t, HealthSources{
				DirectoryForks:    recordLimit(tt.forks, &forkLimit),
				DirectoryFindings: recordLimit(directorySource(automatedLines(tt.automated), nil), &autoLimit),
			})
			if forkLimit != tt.wantForkLimit || autoLimit != tt.wantAutoLimit {
				t.Errorf("source limits: forks %d, automated %d; want %d, %d", forkLimit, autoLimit, tt.wantForkLimit, tt.wantAutoLimit)
			}
			split := strings.Index(detail, "Contact records above known hold")
			if split < 0 {
				t.Fatalf("detail = %q, want an automated-address part", detail)
			}
			forkPart, automatedPart := detail[:split], detail[split:]
			for _, want := range tt.forkPart {
				if !strings.Contains(forkPart, want) {
					t.Errorf("fork part = %q, want it to contain %q", forkPart, want)
				}
			}
			for _, want := range tt.automatedPart {
				if !strings.Contains(automatedPart, want) {
					t.Errorf("automated part = %q, want it to contain %q", automatedPart, want)
				}
			}
			for _, not := range tt.notDetail {
				if strings.Contains(detail, not) {
					t.Errorf("detail = %q, must not contain %q", detail, not)
				}
			}
		})
	}
}

// TestHealthContactDirectoryRow_NamesAtMostFiveInAll pins the row-wide
// bound in every combination of fork and automated-address findings,
// with sources that honour the limit and sources that ignore it: forks
// are named first, automated addresses fill what is left, no more than
// five are named in all, both totals appear, and no part ends in a bare
// count.
func TestHealthContactDirectoryRow_NamesAtMostFiveInAll(t *testing.T) {
	for _, careless := range []bool{false, true} {
		for forks := range 8 {
			for automated := range 8 {
				t.Run(fmt.Sprintf("careless=%v/forks=%d/automated=%d", careless, forks, automated), func(t *testing.T) {
					src := HealthSources{
						DirectoryForks:    directorySource(forkLines(forks), nil),
						DirectoryFindings: directorySource(automatedLines(automated), nil),
					}
					if careless {
						src = HealthSources{DirectoryForks: ignoreLimit(forkLines(forks)), DirectoryFindings: ignoreLimit(automatedLines(automated))}
					}
					detail := contactDirectoryDetail(t, src)
					forksNamed := strings.Count(detail, `name "p`)
					automatedNamed := strings.Count(detail, "(admin, noreply@r")
					if named := forksNamed + automatedNamed; named > maxDirectoryFindingsShown {
						t.Errorf("detail names %d findings, want at most %d: %q", named, maxDirectoryFindingsShown, detail)
					}
					wantForks := min(forks, maxDirectoryFindingsShown)
					wantAutomated := min(automated, maxDirectoryFindingsShown-wantForks)
					if forksNamed != wantForks || automatedNamed != wantAutomated {
						t.Errorf("named %d forks and %d automated addresses, want %d and %d: %q", forksNamed, automatedNamed, wantForks, wantAutomated, detail)
					}
					if forks > 0 && !strings.Contains(detail, fmt.Sprintf("%d duplicate, shared-name or placeholder contact finding", forks)) {
						t.Errorf("detail = %q, want the fork total of %d", detail, forks)
					}
					if automated > 0 && !strings.Contains(detail, fmt.Sprintf("hold %d automated-looking email address", automated)) {
						t.Errorf("detail = %q, want the automated total of %d", detail, automated)
					}
					if strings.Contains(detail, ": (+") {
						t.Errorf("detail = %q, a part ends in a bare count", detail)
					}
				})
			}
		}
	}
}
