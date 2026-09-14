package app

import (
	"context"
	"fmt"
	"log/slog"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

// contactDirectoryRemedy names the operator's fix for a record the
// automated-address cap overrides. Both paths keep the zone in the
// operator's custody.
const contactDirectoryRemedy = "demote the record to known, or move the address to its own known record, through CardDAV (X-THANE-TRUST-ZONE) or PUT /v1/contacts/{id}"

// maxContactDirectoryWarnings bounds the boot Warns one audit emits, so
// a badly misfiled directory cannot flood the log; one summary line
// then carries the total. The audit itself keeps no more findings than
// this. The contact_directory health row counts every finding too but
// names only the first five, so a record past both bounds is counted,
// not named, until earlier ones are fixed.
const maxContactDirectoryWarnings = 20

// maxDirectoryFieldBytes bounds the record name and the address in
// each contact_directory line. Both are free text from the directory;
// clipping them one by one, rather than the whole line, keeps the zone,
// the pattern, and enough of the address to tell which of a record's
// EMAIL values is the automated one, whatever the name's length.
const maxDirectoryFieldBytes = 100

// contactDirectoryFindings formats the first limit records the
// automated-address cap overrides as "<name> (<zone>, <address>:
// <pattern>)", for the contact_directory health row, and returns the
// count of every such record alongside. The name and address are each
// clipped to [maxDirectoryFieldBytes].
func contactDirectoryFindings(ctx context.Context, store *contacts.Store, limit int) ([]string, int, error) {
	audit, err := store.AutomatedAddressesAboveKnown(ctx, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]string, 0, len(audit.Findings))
	for _, f := range audit.Findings {
		out = append(out, fmt.Sprintf("%s (%s, %s: %s)",
			clipDirectoryField(f.Name), f.TrustZone, clipDirectoryField(f.Address), f.Pattern))
	}
	return out, audit.Total, nil
}

// clipDirectoryField cuts s to at most [maxDirectoryFieldBytes] on a
// rune boundary, ending a cut with "…" so the reader can tell the text
// was shortened.
func clipDirectoryField(s string) string {
	return clipDirectoryFieldTo(s, maxDirectoryFieldBytes)
}

// clipDirectoryFieldTo cuts s to at most maxBytes on a rune boundary,
// ending a cut with "…".
func clipDirectoryFieldTo(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	const mark = "…"
	end := max(maxBytes-len(mark), 0)
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + mark
}

// contactDirectoryFindingsSource returns the health source behind the
// contact_directory row, or nil when there is no contact store or email
// polling is off. The row and the boot Warns follow the poller, as
// configuration.md documents; the send gate applies the cap whenever
// email is configured, poll_interval 0 included.
func contactDirectoryFindingsSource(cfg *config.Config, store *contacts.Store) func(context.Context, int) ([]string, int, error) {
	if store == nil || cfg == nil || !emailServicesEnabled(cfg) {
		return nil
	}
	return func(ctx context.Context, limit int) ([]string, int, error) {
		return contactDirectoryFindings(ctx, store, limit)
	}
}

// logContactDirectoryFindings emits one Warn per record above known
// holding an automated-looking email address, at most
// [maxContactDirectoryWarnings] of them followed by one summary, with
// the record name and address each clipped to [maxDirectoryFieldBytes]
// so one oversized directory value cannot inflate the boot log. A
// store failure is logged and never fails boot; the health row reports
// it again on every render.
func logContactDirectoryFindings(ctx context.Context, store *contacts.Store, logger *slog.Logger) {
	audit, err := store.AutomatedAddressesAboveKnown(ctx, maxContactDirectoryWarnings)
	if err != nil {
		logger.Warn("contact directory audit failed; records above known holding automated-looking email addresses were not checked",
			"error", err,
		)
		return
	}
	for _, f := range audit.Findings {
		logger.Warn("contact record above known holds an automated-looking email address; its mail is read at known",
			"contact_id", f.ContactID.String(),
			"contact_name", clipDirectoryField(f.Name),
			"trust_zone", f.TrustZone,
			"address", clipDirectoryField(f.Address),
			"pattern", f.Pattern,
			"read_as", contacts.ZoneKnown,
			"remedy", contactDirectoryRemedy,
		)
	}
	if audit.Total > len(audit.Findings) {
		logger.Warn("more contact records above known hold automated-looking email addresses than were logged; the contact_directory health row counts them all and names the first five",
			"total", audit.Total,
			"logged", len(audit.Findings),
			"read_as", contacts.ZoneKnown,
			"remedy", contactDirectoryRemedy,
		)
	}
}
