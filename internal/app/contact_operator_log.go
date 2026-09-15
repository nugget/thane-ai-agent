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
// names it. When no record was chosen it warns with resolveErr, the
// error the resolution the contact tools pinned returned, so the line
// reports exactly why no operator is pinned. A name that fits several
// records, two that hold it exactly at the same standing or several
// that share it as a first name, is a different fix from a name no
// record holds, and custody refuses every write it guards until it is
// fixed, so the line says which it was. It logs nothing when
// identity.operator_contact_id is configured or no legacy name is.
func logLegacyOperatorResolution(logger *slog.Logger, store *contacts.Store, identity contactIdentityConfig, operatorID uuid.UUID, resolveErr error) {
	name := strings.TrimSpace(identity.legacyOwnerContactName)
	if logger == nil || store == nil || identity.operatorContactID != uuid.Nil || name == "" {
		return
	}
	clippedName := clipDirectoryFieldTo(name, maxForkFieldBytes)
	if operatorID == uuid.Nil {
		attrs := []any{"owner_contact_name", clippedName, "remedy", legacyOperatorRemedy}
		if resolveErr == nil || errors.Is(resolveErr, sql.ErrNoRows) {
			logger.Warn("legacy owner contact name matches no active contact, so no operator contact is pinned", attrs...)
			return
		}
		attrs = append(attrs, "error", resolveErr)
		logger.Warn("legacy owner contact name names no single active contact, so no operator contact is pinned and identity custody refuses every model-facing write it guards until the operator fixes it", attrs...)
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
