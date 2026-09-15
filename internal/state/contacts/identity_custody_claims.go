package contacts

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Notification routing and name custody.
//
// Two contact facts decide where a contact's notifications go:
// ha_companion_app names the Home Assistant device that receives them
// and answers the contact's decision requests, and
// notification_preference picks the channel. Delivery reads each in any
// case, first value first (see [FactValues]). On a contact above known,
// or on the operator's own record, a model-facing writer may not add
// either outside the operator's own message: the target rule addresses
// use. There is no holder rule, because a shared household device or a
// common channel is legitimate.
//
// A name or nickname is what notifications, decision requests, lookups
// and conversation context find a contact by. Changing the nickname or
// the given name of a contact above known, or of the operator's own,
// falls under the same target rule. A given name is a short form the
// record answers to, so an unattended rewrite of it would free the name
// for a known record to take exactly under the short-form rule below.
// In every turn, the operator's own included, no
// model-facing writer gives a new contact a formatted name, or any
// contact a nickname, that an active contact above known or the
// operator's own already goes by, compared the way ResolveContact
// compares them. Where the target rule holds, outside the operator's
// own message and on every import, the same goes for a name such a
// contact answers to by a short form, its given name or the first word
// of its formatted name (see name_keys.go): the record holding a name
// exactly is the one resolution finds, so an exact claim on another
// record's first name would take that name, and its notifications,
// from it. These name claims ride on identityGuard.claims, so the
// check runs inside the write's own transaction.

// Routing fact keys, in the lowercase spelling contact_save stores and
// delivery reads first.
const (
	// PropertyHACompanionApp names the Home Assistant notify service of
	// the device that receives a contact's notifications.
	PropertyHACompanionApp = "ha_companion_app"

	// PropertyNotificationPreference names the notification provider a
	// contact prefers.
	PropertyNotificationPreference = "notification_preference"
)

// Name claims: the contact record's own name fields a model-facing
// writer claims when it creates a contact or sets a nickname.
const (
	claimFN       = "FN"
	claimNickname = "NICKNAME"
)

// Short forms, as [IdentityHolder.Property] names the field a holder
// answers to a refused name claim by when it does not hold the name
// exactly: its given name, or the first word of a formatted name of
// more than one word. A violation's own Property is the claim: claimFN,
// claimNickname, or claimGivenName for a change to an existing
// contact's given name, which has the target rule only.
const (
	claimGivenName   = "GIVEN_NAME"
	claimFNFirstWord = "FN_FIRST_WORD"
)

// isShortFormHolder reports whether a refused name claim's holder
// answers to the name by a short form rather than holding it exactly.
func isShortFormHolder(h *IdentityHolder) bool {
	return h != nil && (h.Property == claimGivenName || h.Property == claimFNFirstWord)
}

// FactValues returns a contact fact's values the way notification
// delivery reads them: props[key] first, then every other spelling of
// key that strings.EqualFold matches, in sorted order. Within one
// spelling the order [Store.GetPropertiesMap] returns (pref, then
// insertion) holds, so the first value is the one delivery uses. A
// hyphenated or otherwise renamed key is a different fact.
func FactValues(props map[string][]string, key string) []string {
	values := append([]string(nil), props[key]...)
	var others []string
	for name := range props {
		if name != key && strings.EqualFold(name, key) {
			others = append(others, name)
		}
	}
	sort.Strings(others)
	for _, name := range others {
		values = append(values, props[name]...)
	}
	return values
}

// routingPropertyFor returns the canonical routing key a property name
// spells, in any case, and true, or "" and false for any other name.
func routingPropertyFor(name string) (string, bool) {
	trimmed := strings.TrimSpace(name)
	for _, key := range []string{PropertyHACompanionApp, PropertyNotificationPreference} {
		if strings.EqualFold(trimmed, key) {
			return key, true
		}
	}
	return "", false
}

// isCustodiedProperty reports whether a property is an address or
// number, or a notification routing fact, in any case.
func isCustodiedProperty(property string) bool {
	_, routing := routingPropertyFor(property)
	return routing || isIdentityProperty(property)
}

// hasCustodiedProperty reports whether any property is custodied, so
// the operator lookup runs only when custody can apply.
func hasCustodiedProperty(props []Property) bool {
	for _, p := range props {
		if isCustodiedProperty(p.Property) {
			return true
		}
	}
	return false
}

// sameFactProperty reports whether two property names are one fact:
// equal, or two spellings of one routing key, which delivery reads as
// one.
func sameFactProperty(a, b string) bool {
	if a == b {
		return true
	}
	ka, okA := routingPropertyFor(a)
	kb, okB := routingPropertyFor(b)
	return okA && okB && ka == kb
}

// sqliteLower folds ASCII letters only, as SQLite's built-in LOWER
// does. It trims nothing, because LOWER does not; [nameKey] trims edge
// space first, as the resolver trims both sides before it compares.
func sqliteLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// sqliteLowerEqual reports whether two names are the same to SQLite
// LOWER without trimming, so an ASCII case-only edit is not a change
// and any other edit, edge space included, is. Whether a record answers
// to a name is the resolver's question, and [nameKey] answers it.
func sqliteLowerEqual(a, b string) bool {
	return sqliteLower(a) == sqliteLower(b)
}

// saveClaims returns the names a contact_save claims, read before the
// save mutates contact: a new contact's formatted name and nickname, or
// an existing contact's changed nickname and changed given name.
// saveContact trims the name and nickname first, so the value judged
// here is the value stored. The given name is stored as written, so it
// is claimed whenever the scalar update would write it with a different
// [nameKey], a blank value that erases the short form included; an edit
// the key folds away, in edge space or ASCII case, changes no name the
// record answers to.
func saveClaims(args SaveContactArgs, contact *Contact, created bool) []Property {
	var claims []Property
	if created {
		claims = append(claims, Property{Property: claimFN, Value: args.Name})
	}
	if strings.TrimSpace(args.Nickname) != "" && (created || !sqliteLowerEqual(args.Nickname, contact.Nickname)) {
		claims = append(claims, Property{Property: claimNickname, Value: args.Nickname})
	}
	if !created && args.GivenName != "" && nameKey(args.GivenName) != nameKey(contact.GivenName) {
		claims = append(claims, Property{Property: claimGivenName, Value: args.GivenName})
	}
	return claims
}

// importClaims returns the names one vCard card claims: a new
// contact's formatted name and nickname, or the nickname a merge fills
// into a contact that has none.
func importClaims(existing, incoming *Contact) []Property {
	if existing == nil {
		return saveClaims(SaveContactArgs{Name: incoming.FormattedName, Nickname: incoming.Nickname}, nil, true)
	}
	if existing.Nickname != "" || strings.TrimSpace(incoming.Nickname) == "" {
		return nil
	}
	return []Property{{Property: claimNickname, Value: incoming.Nickname}}
}

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

// routingViolation applies the target rule to one routing fact. A new
// record takes any value, and a value the target already carries under
// any spelling of the key is a no-op, not a violation.
func routingViolation(query queryFunc, target uuid.UUID, guard identityGuard, key, value string) (*IdentityViolation, error) {
	if target == uuid.Nil {
		return nil, nil
	}
	held, err := routingHeld(query, target, key, value)
	if err != nil || held {
		return nil, err
	}
	if reason := targetCustodyReason(target, guard); reason != "" {
		return &IdentityViolation{Property: key, Value: value, Reason: reason}, nil
	}
	return nil, nil
}

// routingHeld reports whether the target already carries value under
// any spelling of the routing key.
func routingHeld(query queryFunc, target uuid.UUID, key, value string) (bool, error) {
	rows, err := query(`
		SELECT COUNT(*) FROM contact_properties
		WHERE contact_id = ? AND LOWER(property) = ? AND LOWER(value) = LOWER(?)
	`, target.String(), key, value)
	if err != nil {
		return false, fmt.Errorf("check held %s: %w", key, err)
	}
	defer rows.Close()
	var count int
	if rows.Next() {
		if err := rows.Scan(&count); err != nil {
			return false, fmt.Errorf("check held %s: %w", key, err)
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("check held %s: %w", key, err)
	}
	return count > 0, nil
}

// contactSaveOutcome is what one applyContactSave committed.
type contactSaveOutcome struct {
	// changed reports that the save wrote anything. It gates the dossier
	// refresh.
	changed bool

	// routingNote explains each routing value the save inserted that
	// delivery will not use (see routingShadowNotes), or is "".
	routingNote string
}

// readPropertiesMap reads a contact's properties as a property name to
// values map, by name, then pref, then insertion. It is
// [Store.GetPropertiesMap], which delivery reads through, and the read
// a save's shadow note runs inside its own transaction, so the note and
// delivery always order values alike.
func readPropertiesMap(query queryFunc, contactID uuid.UUID) (map[string][]string, error) {
	rows, err := query(
		`SELECT property, value FROM contact_properties WHERE contact_id = ? ORDER BY property, pref NULLS LAST, id`,
		contactID.String())
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	m := make(map[string][]string)
	for rows.Next() {
		var prop, val string
		if err := rows.Scan(&prop, &val); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		m[prop] = append(m[prop], val)
	}
	return m, rows.Err()
}

// routingShadowNotes explains each routing value a save inserted that
// delivery will not use. props is the contact's properties as the
// save's own transaction read them after its inserts (see
// readPropertiesMap), so a value another writer committed after the
// save's snapshot, and before its transaction, is in it; inserted are
// the routing rows the save itself added. The first value is computed
// with [FactValues], exactly as delivery computes it, so a note never
// disagrees with where a notification goes.
func routingShadowNotes(props map[string][]string, inserted []Property) string {
	added := make(map[string][]string)
	for _, p := range inserted {
		if key, ok := routingPropertyFor(p.Property); ok {
			added[key] = append(added[key], p.Value)
		}
	}
	var b strings.Builder
	for _, key := range []string{PropertyHACompanionApp, PropertyNotificationPreference} {
		first := FactValues(props, key)
		if len(first) == 0 {
			continue
		}
		var shadowed []string
		for _, value := range added[key] {
			if value != first[0] {
				shadowed = append(shadowed, value)
			}
		}
		if len(shadowed) == 0 {
			continue
		}
		more := ""
		if len(shadowed) > 1 {
			more = fmt.Sprintf(" (with %d other new value(s))", len(shadowed)-1)
		}
		// ha_companion_app is only the Home Assistant push target, which
		// send_notification reaches after a preference or active channel.
		uses := "notifications still use"
		if key == PropertyHACompanionApp {
			uses = "Home Assistant push still goes to"
		}
		fmt.Fprintf(&b, " %s %q was recorded%s, but %s %q, the first %s value, because facts are additive; to switch, the operator removes %q through CardDAV or the contacts API.",
			key, echoForRefusal(shadowed[0]), more, uses, echoForRefusal(first[0]), key, echoForRefusal(first[0]))
	}
	return b.String()
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
