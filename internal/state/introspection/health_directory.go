package introspection

import (
	"context"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/phasetrace"
)

// maxDirectoryForkBytes bounds each named finding in the fork part of
// the contact_directory detail. A fork line names several records with
// their UUIDs, so it needs more room than an automated-address line;
// the source clips each field first, so this is a backstop for a
// source that does not.
const maxDirectoryForkBytes = 512

// directoryRow assembles the contact_directory lamp from the two
// directory audits, either of which may be unwired, and reports false
// when neither is. Findings or a failed audit from either one degrade
// the row, and the detail carries both parts, the fork part first.
//
// The parts share the row's [maxDirectoryFindingsShown] named findings.
// The fork audit is asked for all of them and the automated-address
// audit for what the fork part left, which may be none; each source is
// still called, so both totals appear, and whatever a source returns is
// clipped to the limit it was given.
func (i *Inspector) directoryRow(ctx context.Context) (HealthRow, bool) {
	if i.src.DirectoryFindings == nil && i.src.DirectoryForks == nil {
		return HealthRow{}, false
	}
	budget := maxDirectoryFindingsShown
	var parts []string
	if i.src.DirectoryForks != nil {
		done := phasetrace.Phase(ctx, "health:directory_forks")
		shown, total, err := i.src.DirectoryForks(ctx, budget)
		done()
		switch {
		case err != nil:
			parts = append(parts, fmt.Sprintf("The contact fork audit failed: %v.", err))
		case total > 0:
			shown = shown[:min(len(shown), budget)]
			budget -= len(shown)
			parts = append(parts, directoryForksDetail(shown, total))
		}
	}
	if i.src.DirectoryFindings != nil {
		done := phasetrace.Phase(ctx, "health:directory_findings")
		shown, total, err := i.src.DirectoryFindings(ctx, budget)
		done()
		switch {
		case err != nil:
			parts = append(parts, fmt.Sprintf("The contact directory audit failed: %v.", err))
		case total > 0:
			shown = shown[:min(len(shown), budget)]
			parts = append(parts, directoryFindingsDetail(shown, total))
		}
	}
	row := HealthRow{Name: "contact_directory", Status: HealthOK}
	if len(parts) > 0 {
		row.Status = HealthDegraded
		row.Detail = strings.Join(parts, " ")
	}
	return row, true
}

// directoryFindingList ends one part of the contact_directory detail:
// ": " and the named findings, each clipped to maxBytes, with the rest
// of total counted as "(+N more)"; or, when the part names none of its
// findings, a clause saying so and why, so the part never ends in a
// bare count.
func directoryFindingList(shown []string, total, maxBytes int) string {
	if len(shown) == 0 {
		subject := "none is named"
		if total == 1 {
			subject = "it is not named"
		}
		return fmt.Sprintf("; %s here, since the contact_directory row names at most %d findings in all and names duplicate, shared-name or placeholder findings first", subject, maxDirectoryFindingsShown)
	}
	clipped := make([]string, 0, len(shown))
	for _, line := range shown {
		clipped = append(clipped, clipUTF8(line, maxBytes))
	}
	list := ": " + strings.Join(clipped, "; ")
	if extra := total - len(shown); extra > 0 {
		list += fmt.Sprintf(" (+%d more)", extra)
	}
	return list
}

// directoryForksDetail renders the fork part of the contact_directory
// detail: how many duplicate, shared-name or placeholder findings the
// directory holds, the ones the row names, the fix for each kind, and
// which of those fixes Thane can make itself. total counts every
// finding; shown is what the row names, already within its budget.
func directoryForksDetail(shown []string, total int) string {
	plural := "s"
	if total == 1 {
		plural = ""
	}
	return fmt.Sprintf("%d duplicate, shared-name or placeholder contact finding%s can send a name lookup, a message, presence or a dossier to the wrong record%s. For records likely to be one person, merge the duplicate into the record that should keep the name or address; for different people who answer to one name, rename one or give it a distinct nickname; for an address different people share, move it to the record that owns it; for a placeholder, replace it with the real address or remove it. The operator does these through CardDAV or /v1/contacts. Thane can forget a known duplicate that is neither the operator's nor bound to a Home Assistant person, by contact_id, and can copy a duplicate's addresses onto a record above known only in the operator's own message; it renames nothing and removes no address.",
		total, plural, directoryFindingList(shown, total, maxDirectoryForkBytes))
}
