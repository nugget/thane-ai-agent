package app

import (
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
// record and whether the name matched its formatted name, its nickname
// or only the search. It logs nothing when identity.operator_contact_id
// is configured or no legacy name is.
func logLegacyOperatorResolution(logger *slog.Logger, store *contacts.Store, identity contactIdentityConfig, operatorID uuid.UUID) {
	name := strings.TrimSpace(identity.legacyOwnerContactName)
	if logger == nil || store == nil || identity.operatorContactID != uuid.Nil || name == "" {
		return
	}
	clippedName := clipDirectoryFieldTo(name, maxForkFieldBytes)
	if operatorID == uuid.Nil {
		logger.Warn("legacy owner contact name matches no active contact, so no operator contact is pinned",
			"owner_contact_name", clippedName,
			"remedy", legacyOperatorRemedy,
		)
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
		"matched_by", legacyOperatorMatch(name, operator),
		"remedy", legacyOperatorRemedy,
	)
}

// legacyOperatorMatch says how name reached operator: "formatted_name",
// "nickname", or "search", folding ASCII letters as the resolver's LOWER
// does.
func legacyOperatorMatch(name string, operator *contacts.Contact) string {
	key := asciiLowerTrim(name)
	switch key {
	case asciiLowerTrim(operator.FormattedName):
		return "formatted_name"
	case asciiLowerTrim(operator.Nickname):
		return "nickname"
	}
	return "search"
}

func asciiLowerTrim(s string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, strings.TrimSpace(s))
}
