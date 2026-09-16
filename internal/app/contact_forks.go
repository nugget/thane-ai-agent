package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/state/introspection"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// contactForkRemedyByKind names the fix for each kind of fork finding,
// and who can apply it. No model-facing tool renames a contact, removes
// or replaces an address, or moves a Home Assistant person binding, so
// those remedies are the operator's; the one directory fix Thane can
// make itself is forgetting a known, unbound duplicate by contact_id.
var contactForkRemedyByKind = map[string]string{
	contacts.ForkKindName:           "if the records are one person, merge the duplicate into the record that should keep the name; if they are different people, rename one. The operator does either through CardDAV or /v1/contacts. Thane can forget a known duplicate that is neither the operator's nor bound to a Home Assistant person, by contact_id, and copy its addresses onto a record above known only in the operator's own message. Forgetting the only record whose formatted name or nickname is the name leaves the name to the one record whose given name or first word it is, and to none while several have one, so the operator gives the record that keeps it the name as a nickname through CardDAV or /v1/contacts; Thane sets that nickname only when the operator asks in their own message",
	contacts.ForkKindSharedName:     "give one of the records a distinct name or nickname through CardDAV or /v1/contacts, so a name lookup can reach each; merge them only if they are one person",
	contacts.ForkKindEmail:          "move the address to the one record that owns it, or merge the duplicate into that record, through CardDAV or /v1/contacts. Thane can forget a known duplicate that is neither the operator's nor bound to a Home Assistant person, by contact_id",
	contacts.ForkKindPhone:          "move the number to the one record that owns it, or merge the duplicate into that record, through CardDAV or /v1/contacts. Thane can forget a known duplicate that is neither the operator's nor bound to a Home Assistant person, by contact_id",
	contacts.ForkKindReservedDomain: "replace the placeholder with the real address, or remove it, through CardDAV or PUT /v1/contacts/{id}; no model-facing tool removes or replaces an address",
}

// contactForkWarningByKind is the boot Warn message for each kind.
var contactForkWarningByKind = map[string]string{
	contacts.ForkKindName:           "contact records that look like one person share a name with a record above known or the operator's own; a name lookup, presence or a dossier can land on the duplicate",
	contacts.ForkKindSharedName:     "different contact records answer to one name, one of them above known or the operator's own; a name lookup reaches only the one with the most standing, or none of them when two or more share it",
	contacts.ForkKindEmail:          "contact records share an email address with a record above known or the operator's own; mail to or from it can land on the duplicate or be gated at the wrong zone",
	contacts.ForkKindPhone:          "contact records share a phone number with a record above known or the operator's own; a Signal sender on it can bind to the duplicate or to no contact",
	contacts.ForkKindReservedDomain: "contact record above known or of the operator holds a placeholder email address on a reserved domain, where no mailbox exists",
}

// contactForkRemedy names the fix for one kind of fork finding.
func contactForkRemedy(kind string) string {
	if remedy, ok := contactForkRemedyByKind[kind]; ok {
		return remedy
	}
	return contactForkRemedyByKind[contacts.ForkKindName]
}

// maxForkFieldBytes bounds each free-text field (a record name, a key, a
// zone) in a fork line or boot Warn. A fork line names several records
// with their UUIDs, so its fields are clipped tighter than the
// automated-address lines, and a line naming [maxForkMembersShown]
// records stays inside the health row's per-line backstop.
const maxForkFieldBytes = 48

// maxForkMembersShown bounds the records one fork line names; the rest
// are counted.
const maxForkMembersShown = 3

// logContactDirectoryAudits emits the boot Warns of both directory
// audits: the automated-address audit only while email polling is on,
// as its health row does, and the fork audit whenever there is a
// contact store, since a fork misroutes name lookups, presence and
// dossiers whichever channels are configured.
func logContactDirectoryAudits(ctx context.Context, cfg *config.Config, store *contacts.Store, logger *slog.Logger) {
	if store == nil {
		return
	}
	if cfg != nil && emailServicesEnabled(cfg) {
		logContactDirectoryFindings(ctx, store, logger)
	}
	logContactForkFindings(ctx, store, logger)
}

// wireContactDirectoryHealth sets both sources of the contact_directory
// health row: the automated-address audit, which follows email polling,
// and the fork audit, which runs whenever there is a contact store.
func wireContactDirectoryHealth(src *introspection.HealthSources, cfg *config.Config, store *contacts.Store) {
	src.DirectoryFindings = contactDirectoryFindingsSource(cfg, store)
	src.DirectoryForks = contactForkFindingsSource(store)
}

// contactForkFindingsSource returns the health source behind the fork
// part of the contact_directory row, or nil when there is no contact
// store. Unlike the automated-address source it does not follow email
// polling.
func contactForkFindingsSource(store *contacts.Store) func(context.Context, int) ([]string, int, error) {
	if store == nil {
		return nil
	}
	return func(ctx context.Context, limit int) ([]string, int, error) {
		return contactForkFindings(ctx, store, limit)
	}
}

// contactForkFindings formats the first limit fork findings, one line
// each, for the contact_directory health row, and returns the count of
// every finding alongside.
func contactForkFindings(ctx context.Context, store *contacts.Store, limit int) ([]string, int, error) {
	audit, err := store.ContactForks(ctx, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]string, 0, len(audit.Findings))
	for _, f := range audit.Findings {
		out = append(out, formatContactFork(f))
	}
	return out, audit.Total, nil
}

// formatContactFork renders one finding as `name "alice" likely one
// person: Alice Smith (household, <uuid>, HA person) and Alice (known,
// <uuid>)`, `name "mom" answered to by different people: ...`,
// `email a@b.net shared by ...`, or `placeholder address a@example.com
// on Alice (admin, <uuid>)`.
func formatContactFork(f contacts.ContactForkFinding) string {
	key := clipDirectoryFieldTo(f.Key, maxForkFieldBytes)
	switch f.Kind {
	case contacts.ForkKindReservedDomain:
		return fmt.Sprintf("placeholder address %s on %s", key, formatForkMembers(f))
	case contacts.ForkKindName:
		return fmt.Sprintf("name %q likely one person: %s", key, formatForkMembers(f))
	case contacts.ForkKindSharedName:
		return fmt.Sprintf("name %q answered to by different people: %s", key, formatForkMembers(f))
	default:
		return fmt.Sprintf("%s %s shared by %s", f.Kind, key, formatForkMembers(f))
	}
}

// formatForkMembers names the first [maxForkMembersShown] records of a
// finding and counts the rest.
func formatForkMembers(f contacts.ContactForkFinding) string {
	shown := f.Members[:min(len(f.Members), maxForkMembersShown)]
	parts := make([]string, 0, len(shown))
	for _, m := range shown {
		parts = append(parts, formatForkMember(m))
	}
	if extra := f.MemberCount - len(shown); extra > 0 {
		return strings.Join(parts, ", ") + fmt.Sprintf(" and %d more", extra)
	}
	if len(parts) <= 2 {
		return strings.Join(parts, " and ")
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// formatForkMember renders one record as "<name> (<zone>, <uuid>)",
// adding "operator" for the operator's own record and "HA person" for
// one bound to a Home Assistant person.
func formatForkMember(m contacts.ContactForkMember) string {
	attrs := []string{clipDirectoryFieldTo(m.TrustZone, maxForkFieldBytes), m.ContactID.String()}
	if m.Operator {
		attrs = append(attrs, "operator")
	}
	if m.HAPersonEntity != "" {
		attrs = append(attrs, "HA person")
	}
	return fmt.Sprintf("%s (%s)", clipDirectoryFieldTo(m.Name, maxForkFieldBytes), strings.Join(attrs, ", "))
}

// logContactForkFindings emits one Warn per fork finding, at most
// [maxContactDirectoryWarnings] of them followed by one summary, with
// every free-text field clipped. A store failure is logged and never
// fails boot; the health row reports it again on every render.
func logContactForkFindings(ctx context.Context, store *contacts.Store, logger *slog.Logger) {
	audit, err := store.ContactForks(ctx, maxContactDirectoryWarnings)
	if err != nil {
		logger.Warn("contact fork audit failed; duplicate, shared-name and placeholder contact records were not checked",
			"error", err,
		)
		return
	}
	for _, f := range audit.Findings {
		if f.Kind == contacts.ForkKindReservedDomain {
			m := f.Members[0]
			logger.Warn(contactForkWarningByKind[f.Kind],
				"contact_id", m.ContactID.String(),
				"contact_name", clipDirectoryFieldTo(m.Name, maxForkFieldBytes),
				"trust_zone", clipDirectoryFieldTo(m.TrustZone, maxForkFieldBytes),
				"operator", m.Operator,
				"address", clipDirectoryFieldTo(f.Key, maxForkFieldBytes),
				"remedy", contactForkRemedy(f.Kind),
			)
			continue
		}
		message, ok := contactForkWarningByKind[f.Kind]
		if !ok {
			message = contactForkWarningByKind[contacts.ForkKindName]
		}
		logger.Warn(message,
			"kind", f.Kind,
			"key", clipDirectoryFieldTo(f.Key, maxForkFieldBytes),
			"member_count", f.MemberCount,
			"members", formatForkMembers(f),
			"remedy", contactForkRemedy(f.Kind),
		)
	}
	if audit.Total > len(audit.Findings) {
		logger.Warn("more duplicate, shared-name or placeholder contact findings exist than were logged; the contact_directory health row counts them all, names the first five, and says how to fix each kind",
			"total", audit.Total,
			"logged", len(audit.Findings),
		)
	}
}

// configureContactDossierDocuments hands the contact tools the document
// tools a dossier is read and written through, the store-level presence
// check the second-dossier refusal uses, and the archive lookup a
// refused session-id citation resolves through. Without document tools,
// dossier reads and writes stay unconfigured.
func configureContactDossierDocuments(contactTools *contacts.Tools, docTools *documents.Tools, docStore *documents.Store, archive *memory.ArchiveStore) {
	if contactTools == nil || docTools == nil {
		return
	}
	contactTools.ConfigureDossierDocuments(docTools.Read, docTools.WriteFaceted)
	contactTools.ConfigureDossierPresence(documentPresence(docStore))
	contactTools.ConfigureDossierArchiveSessions(archiveSessionResolver(archive))
}

// documentPresence reports whether a document exists at a ref, for the
// second-dossier refusal. It reads through the store rather than the
// document tools, so the check records no read receipt. A document the
// store finds but cannot serve is an error, which the refusal reports
// rather than guessing. It is nil when there is no document store.
func documentPresence(store *documents.Store) func(context.Context, string) (bool, error) {
	if store == nil {
		return nil
	}
	return func(ctx context.Context, ref string) (bool, error) {
		_, err := store.Read(ctx, ref)
		if documents.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return true, nil
	}
}
