package contacts

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Name claims: the target rule and the holder rule a formatted name,
// nickname or given name must pass before a model-facing write (see
// identity_custody_claims.go for the routing facts and the claim
// properties). The holder rule compares names as the resolver does.

// isClaimProperty reports whether a violation is a name claim.
func isClaimProperty(property string) bool {
	return property == claimFN || property == claimNickname || property == claimGivenName
}

// claimArgument names the contact_save argument a name claim came from.
func claimArgument(property string) string {
	switch property {
	case claimFN:
		return "name"
	case claimGivenName:
		return "given_name"
	}
	return "nickname"
}

// hasClaimViolation reports whether any violation is a name claim.
func hasClaimViolation(violations []IdentityViolation) bool {
	for _, v := range violations {
		if isClaimProperty(v.Property) {
			return true
		}
	}
	return false
}

// claimViolations applies the name rules to guard.claims: the target
// rule to a nickname or given-name change on an existing target, then
// the holder rule to every formatted name and nickname claim.
//
// A given name gets the target rule alone. It is not a name another
// record can be found by instead of the holder: a known record sharing
// a first name with a custodied one leaves the name reaching neither,
// and the short-form claim rule already stops a known record holding it
// exactly. What the target rule stops is the custodied record losing
// the short form, which is what would let such a claim through.
func claimViolations(query queryFunc, target uuid.UUID, guard identityGuard) ([]IdentityViolation, error) {
	var violations []IdentityViolation
	for _, c := range guard.claims {
		if c.Property == claimGivenName {
			if reason := targetNameReason(target, guard); reason != "" {
				violations = append(violations, IdentityViolation{Property: c.Property, Value: c.Value, Reason: reason})
			}
			continue
		}
		if strings.TrimSpace(c.Value) == "" {
			continue
		}
		if c.Property == claimNickname {
			if reason := targetNameReason(target, guard); reason != "" {
				violations = append(violations, IdentityViolation{Property: c.Property, Value: c.Value, Reason: reason})
				continue
			}
		}
		holder, err := nameHolder(query, target, guard, c.Value)
		if err != nil {
			return nil, err
		}
		if holder != nil {
			violations = append(violations, IdentityViolation{Property: c.Property, Value: c.Value, Reason: IdentityReasonHolder, Holder: holder})
		}
	}
	return violations, nil
}

// targetNameReason applies the target rule to a name change on an
// existing target, or returns "" for a record being created. An import
// card whose operator lookup failed counts every target as the
// operator's, so the rule fails closed there.
func targetNameReason(target uuid.UUID, guard identityGuard) string {
	if target == uuid.Nil {
		return ""
	}
	reason := targetCustodyReason(target, guard)
	if reason == "" && guard.operatorUnresolved && !guard.liftTargetCustody {
		reason = IdentityReasonOperator
	}
	return reason
}

// nameHolder returns the active record other than target, carrying
// authority, that name would be taken from, or nil. Authority is a zone
// other than known (a malformed zone counts) or the operator's own
// record, and when the operator could not be resolved every record
// counts, so the rule fails closed.
//
// Names are compared as the resolver and the fork audit compare them,
// through [recordNameKeys] and [nameKey]: both sides trimmed of every
// edge space rune and folded with LOWER. So a name an authority record
// stores with a tab or a no-break space at its edge stays its own. A
// record that holds the name exactly, as its formatted name or
// nickname, counts in every turn. A record that answers to it only by a
// short form, its given name or the first word of its formatted name,
// counts where the target rule holds (guard.liftTargetCustody unset):
// the claimed name would make the target an exact holder, which
// resolution finds before any short form. An exact holder is returned
// before a short-form one, and within each by formatted name and id;
// the holder's Property names the field it answers by.
func nameHolder(query queryFunc, target uuid.UUID, guard identityGuard, name string) (*IdentityHolder, error) {
	key := nameKey(name)
	if key == "" {
		return nil, nil
	}
	records, err := authorityNameRecords(query, target, guard)
	if err != nil {
		return nil, err
	}
	var short *IdentityHolder
	for _, r := range records {
		switch field := r.keys.matchField(key); field {
		case "":
		case NameFieldFormatted, NameFieldNickname:
			h := r.heldAs(field)
			return &h, nil
		default:
			if short == nil && !guard.liftTargetCustody {
				h := r.heldAs(field)
				short = &h
			}
		}
	}
	return short, nil
}

// nameClaimRecord is one active record with authority as the name rules
// read it: the holder a refusal names, the stored names a refusal
// echoes, and the record's name keys.
type nameClaimRecord struct {
	holder          IdentityHolder
	nickname, given string
	keys            nameKeys
}

// heldAs is r as the holder of a refused name claim, answering by field
// (one of the NameField values [nameKeys.matchField] returns), with the
// stored value it answers by.
func (r nameClaimRecord) heldAs(field string) IdentityHolder {
	h := r.holder
	switch field {
	case NameFieldFormatted:
		h.Property, h.Value = claimFN, h.Name
	case NameFieldNickname:
		h.Property, h.Value = claimNickname, r.nickname
	case NameFieldGiven:
		h.Property, h.Value = claimGivenName, r.given
	default:
		// matchField names the first word only for a formatted name of
		// more than one word, so Fields has one.
		h.Property, h.Value = claimFNFirstWord, strings.Fields(h.Name)[0]
	}
	return h
}

// authorityNameRecords reads every active record other than target that
// carries authority as nameHolder counts it, by formatted name and id,
// marking the operator's own. Rows are read fully so no cursor stays
// open inside a transaction.
func authorityNameRecords(query queryFunc, target uuid.UUID, guard identityGuard) ([]nameClaimRecord, error) {
	operator, everyRecord := "", 0
	if guard.operatorID != uuid.Nil {
		operator = guard.operatorID.String()
	}
	if guard.operatorUnresolved {
		everyRecord = 1
	}
	rows, err := query(`
		SELECT id, COALESCE(formatted_name, ''), COALESCE(nickname, ''), COALESCE(given_name, ''), COALESCE(trust_zone, '')
		FROM contacts
		WHERE deleted_at IS NULL
		  AND id <> ?
		  AND (? = 1 OR id = ? OR COALESCE(trust_zone, '') <> ?)
		ORDER BY formatted_name, id
	`, target.String(), everyRecord, operator, ZoneKnown)
	if err != nil {
		return nil, fmt.Errorf("find name holders: %w", err)
	}
	defer rows.Close()
	var records []nameClaimRecord
	for rows.Next() {
		var id string
		var r nameClaimRecord
		if err := rows.Scan(&id, &r.holder.Name, &r.nickname, &r.given, &r.holder.Zone); err != nil {
			return nil, fmt.Errorf("scan name holder: %w", err)
		}
		if r.holder.ID, err = uuid.Parse(id); err != nil {
			return nil, fmt.Errorf("parse name holder id %q: %w", id, err)
		}
		r.holder.Operator = operator != "" && id == operator
		r.keys = recordNameKeys(r.holder.Name, r.nickname, r.given)
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find name holders: %w", err)
	}
	return records, nil
}

// withoutAuthorityNameClaim applies the name rules to one vCard card
// before its write. A new card whose name or nickname one of them
// refuses is skipped whole; a merge leaves the refused nickname fill
// out and drops its claim from guard. It returns the refused claims,
// which the import counts and logs; applyContactImport rechecks the
// kept claims inside the card's write.
func (t *Tools) withoutAuthorityNameClaim(query queryFunc, target uuid.UUID, guard *identityGuard, incoming *Contact) ([]IdentityViolation, error) {
	if len(guard.claims) == 0 {
		return nil, nil
	}
	violations, err := claimViolations(query, target, *guard)
	if err != nil {
		return nil, fmt.Errorf("check name custody: %w", err)
	}
	if len(violations) > 0 && target != uuid.Nil {
		incoming.Nickname = ""
		guard.claims = nil
	}
	return violations, nil
}
