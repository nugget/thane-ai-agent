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
