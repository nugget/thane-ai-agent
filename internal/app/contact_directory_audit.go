package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

// contactDirectoryRemedy names the operator's fix for a record the
// automated-address cap overrides. Both paths keep the zone in the
// operator's custody.
const contactDirectoryRemedy = "demote the record to known, or move the address to its own known record, through CardDAV (X-THANE-TRUST-ZONE) or PUT /v1/contacts/{id}"

// maxContactDirectoryWarnings bounds the boot Warns one audit emits, so
// a badly misfiled directory cannot flood the log; one summary line
// then carries the total. The contact_directory health row counts every
// finding too but names only the first five, so a record past both
// bounds is counted, not named, until earlier ones are fixed.
const maxContactDirectoryWarnings = 20

// contactDirectoryFindings formats each record the automated-address
// cap overrides as "<name> (<zone>, <address>: <pattern>)", for the
// contact_directory health row.
func contactDirectoryFindings(ctx context.Context, store *contacts.Store) ([]string, error) {
	findings, err := store.AutomatedAddressesAboveKnown(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, fmt.Sprintf("%s (%s, %s: %s)", f.Name, f.TrustZone, f.Address, f.Pattern))
	}
	return out, nil
}

// contactDirectoryFindingsSource returns the health source behind the
// contact_directory row, or nil when there is no contact store or email
// polling is off. The row and the boot Warns follow the poller, as
// configuration.md documents; the send gate applies the cap whenever
// email is configured, poll_interval 0 included.
func contactDirectoryFindingsSource(cfg *config.Config, store *contacts.Store) func(context.Context) ([]string, error) {
	if store == nil || cfg == nil || !emailServicesEnabled(cfg) {
		return nil
	}
	return func(ctx context.Context) ([]string, error) {
		return contactDirectoryFindings(ctx, store)
	}
}

// logContactDirectoryFindings emits one Warn per record above known
// holding an automated-looking email address, at most
// [maxContactDirectoryWarnings] of them followed by one summary. A
// store failure is logged and never fails boot; the health row reports
// it again on every render.
func logContactDirectoryFindings(ctx context.Context, store *contacts.Store, logger *slog.Logger) {
	findings, err := store.AutomatedAddressesAboveKnown(ctx)
	if err != nil {
		logger.Warn("contact directory audit failed; records above known holding automated-looking email addresses were not checked",
			"error", err,
		)
		return
	}
	for i, f := range findings {
		if i == maxContactDirectoryWarnings {
			logger.Warn("more contact records above known hold automated-looking email addresses than were logged; the contact_directory health row counts them all and names the first five",
				"total", len(findings),
				"logged", maxContactDirectoryWarnings,
				"read_as", contacts.ZoneKnown,
				"remedy", contactDirectoryRemedy,
			)
			return
		}
		logger.Warn("contact record above known holds an automated-looking email address; its mail is read at known",
			"contact_id", f.ContactID.String(),
			"contact_name", f.Name,
			"trust_zone", f.TrustZone,
			"address", f.Address,
			"pattern", f.Pattern,
			"read_as", contacts.ZoneKnown,
			"remedy", contactDirectoryRemedy,
		)
	}
}
