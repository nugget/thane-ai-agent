package app

import (
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

// legacyOperatorRemedy is how the operator stops the legacy owner
// contact name from choosing a record by name.
const legacyOperatorRemedy = "set identity.operator_contact_id to the operator's contact UUID to pin the record by id"

// logLegacyOperatorResolution records at startup which contact the
// legacy owner contact name chose as the operator's. The name resolves
// before any pin exists, authority first, so on an upgrade it can move
// the operator to a record above known that goes by the name as a
// nickname; custody, IsOwner and name resolution then treat that record
// as the operator's for the life of the process. The line names the
// record and the name field the name matched, as [contacts.NameMatchField]
// names it. When no record was chosen it warns, with the resolver's
// error when the name fits several records and names none of them. It
// logs nothing when identity.operator_contact_id is configured or no
// legacy name is.
func logLegacyOperatorResolution(logger *slog.Logger, store *contacts.Store, identity contactIdentityConfig, operatorID uuid.UUID) {
	name := strings.TrimSpace(identity.legacyOwnerContactName)
	if logger == nil || store == nil || identity.operatorContactID != uuid.Nil || name == "" {
		return
	}
	clippedName := clipDirectoryFieldTo(name, maxForkFieldBytes)
	if operatorID == uuid.Nil {
		attrs := []any{"owner_contact_name", clippedName, "remedy", legacyOperatorRemedy}
		// A first name several records share is a different fix from a
		// name no record holds, so say which it was.
		if _, err := store.ResolveContact(name); err != nil && !errors.Is(err, sql.ErrNoRows) {
			attrs = append(attrs, "error", err)
		}
		logger.Warn("legacy owner contact name matches no active contact, or matches several and names none of them, so no operator contact is pinned", attrs...)
		return
	}
	operator, err := store.Get(operatorID)
	if err != nil {
		logger.Warn("could not load the contact the legacy owner contact name chose as the operator's",
			"owner_contact_name", clippedName,
			"contact_id", operatorID.String(),
			"error", err,
		)
		return
	}
	logger.Info("legacy owner contact name chose the operator's contact; custody, IsOwner and name resolution treat this record as the operator's",
		"owner_contact_name", clippedName,
		"contact_id", operator.ID.String(),
		"contact_name", clipDirectoryFieldTo(operator.FormattedName, maxForkFieldBytes),
		"trust_zone", clipDirectoryFieldTo(operator.TrustZone, maxForkFieldBytes),
		"matched_by", contacts.NameMatchField(operator, name),
		"remedy", legacyOperatorRemedy,
	)
}
