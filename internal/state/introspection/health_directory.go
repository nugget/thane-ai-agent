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
// the row, and the detail carries both parts.
func (i *Inspector) directoryRow(ctx context.Context) (HealthRow, bool) {
	if i.src.DirectoryFindings == nil && i.src.DirectoryForks == nil {
		return HealthRow{}, false
	}
	var parts []string
	if i.src.DirectoryFindings != nil {
		done := phasetrace.Phase(ctx, "health:directory_findings")
		shown, total, err := i.src.DirectoryFindings(ctx, maxDirectoryFindingsShown)
		done()
		switch {
		case err != nil:
			parts = append(parts, fmt.Sprintf("contact directory audit failed: %v", err))
		case total > 0:
			parts = append(parts, directoryFindingsDetail(shown, total))
		}
	}
	if i.src.DirectoryForks != nil {
		done := phasetrace.Phase(ctx, "health:directory_forks")
		shown, total, err := i.src.DirectoryForks(ctx, maxDirectoryFindingsShown)
		done()
		switch {
		case err != nil:
			parts = append(parts, fmt.Sprintf("contact fork audit failed: %v", err))
		case total > 0:
			parts = append(parts, directoryForksDetail(shown, total))
		}
	}
	row := HealthRow{Name: "contact_directory", Status: HealthOK}
	if len(parts) > 0 {
		row.Status = HealthDegraded
		row.Detail = strings.Join(parts, " ")
	}
	return row, true
}

// directoryForksDetail renders the fork part of the contact_directory
// detail: how many duplicate, shared-name or placeholder findings the
// directory holds, the first few of them, the fix for each kind, and
// which of those fixes Thane can make itself. total counts every
// finding; shown is the prefix the source returned.
func directoryForksDetail(shown []string, total int) string {
	shown = shown[:min(len(shown), maxDirectoryFindingsShown)]
	clipped := make([]string, 0, len(shown))
	for _, line := range shown {
		clipped = append(clipped, clipUTF8(line, maxDirectoryForkBytes))
	}
	list := strings.Join(clipped, "; ")
	if extra := total - len(shown); extra > 0 {
		list += fmt.Sprintf(" (+%d more)", extra)
	}
	plural := "s"
	if total == 1 {
		plural = ""
	}
	return fmt.Sprintf("%d duplicate, shared-name or placeholder contact finding%s can send a name lookup, a message, presence or a dossier to the wrong record: %s. For records likely to be one person, merge the duplicate into the record that should keep the name or address; for different people who answer to one name, rename one or give it a distinct nickname; for an address different people share, move it to the record that owns it; for a placeholder, replace it with the real address or remove it. The operator does these through CardDAV or /v1/contacts. Thane can forget a known duplicate that is neither the operator's nor bound to a Home Assistant person, by contact_id, and can copy a duplicate's addresses onto a record above known only in the operator's own message; it renames nothing and removes no address.",
		total, plural, list)
}
