package contacts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

// custodyOperatorID returns the operator's own record for identity
// custody: the configured operator_contact_id, or the record the legacy
// owner name resolved to. The app pins that resolution at startup with
// [Tools.ConfigureLegacyOperatorContactID], sharing the channel
// resolver's cached answer, so custody protects exactly the contact
// that carries IsOwner. Unpinned, the legacy name is resolved here with
// the same ResolveContact the resolver uses. uuid.Nil means no operator
// is configured, or no contact answers to the legacy name; the
// sole-admin fallback is already custodied by its zone.
//
// Any other resolution failure fails closed, pinned or not, an ended
// ctx included. A legacy name several contacts answer to, as a tie or a
// shared first name, is the case that matters: the operator is one of
// those contacts and custody cannot tell which, and protecting none
// would let an unattended write, a nickname change or a forget, decide
// which of them the next start makes the operator, with every address a
// write gave it.
func (t *Tools) custodyOperatorID(ctx context.Context) (uuid.UUID, error) {
	if t.operatorContactID != uuid.Nil {
		return t.operatorContactID, nil
	}
	name := strings.TrimSpace(t.ownerContactName)
	if t.legacyOperatorPinned {
		if err := t.legacyOperatorUnresolved(); err != nil {
			return uuid.Nil, custodyUnresolvedError(name, true, err)
		}
		return t.legacyOperatorID, nil
	}
	if name == "" {
		return uuid.Nil, nil
	}
	operator, err := t.store.resolveContact(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, custodyUnresolvedError(name, false, err)
	}
	return operator.ID, nil
}

// legacyOwnerUnresolvedError says why the legacy owner name named no
// single contact, and how the operator fixes it. pinned says the name
// was resolved when Thane started, so a fix takes a restart. A name
// several contacts answer to lists them with their contact_id values,
// without the retry an [AmbiguousNameError] ends with, since no tool
// argument settles which contact is the operator's.
func legacyOwnerUnresolvedError(owner string, pinned bool, err error) error {
	when, restart := "now", ""
	if pinned {
		when, restart = "when Thane started", " and restart Thane"
	}
	var amb *AmbiguousNameError
	if !errors.As(err, &amb) {
		return fmt.Errorf("the name Thane recognizes the operator by (identity.owner_contact_name %q) could not be resolved %s: %w", echoForRefusal(owner), when, err)
	}
	return fmt.Errorf("the name Thane recognizes the operator by (identity.owner_contact_name %q) named no single contact %s, so Thane cannot tell which contact is the operator's own: %s. Ask the operator to set identity.operator_contact_id to their own contact's UUID, or to make their own contact the only one whose formatted name or nickname is that name, through CardDAV or the contacts API%s",
		echoForRefusal(owner), when, amb.summary(), restart)
}

// custodyUnresolvedError is custody's refusal when the legacy owner name
// names no single contact. Every write that needs the operator's record
// is refused, which is every write custody guards.
func custodyUnresolvedError(owner string, pinned bool, err error) error {
	var amb *AmbiguousNameError
	if !errors.As(err, &amb) {
		return fmt.Errorf("check identity custody: %w", legacyOwnerUnresolvedError(owner, pinned, err))
	}
	return fmt.Errorf("check identity custody: nothing was changed, because %w. Until the operator does, no model-facing write creates a contact, sets a nickname, changes a given name, adds an address, number or notification routing fact, or forgets a contact, because the operator's own contact is one of these and protecting none of them would let such a write decide which one Thane takes as the operator; tell the operator",
		legacyOwnerUnresolvedError(owner, pinned, err))
}

// legacyOwnerName returns the legacy owner name when it is the operator
// selector, that is when no operator_contact_id is configured, or "".
func (t *Tools) legacyOwnerName() string {
	if t.operatorContactID != uuid.Nil {
		return ""
	}
	return strings.TrimSpace(t.ownerContactName)
}

// claimsOwnerName reports whether any of names is the legacy owner name,
// ignoring edge space and Unicode case. That matches more than the
// resolver does, which is safe only where a match refuses a claim;
// whether a record answers to the name uses the resolver's [nameKey].
func claimsOwnerName(owner string, names ...string) bool {
	if owner == "" {
		return false
	}
	for _, name := range names {
		if strings.EqualFold(strings.TrimSpace(name), owner) {
			return true
		}
	}
	return false
}

// ownerNameClaimRefusal checks a contact_save that would give a record
// other than the operator's own the legacy owner name. Under the legacy
// selector the operator is whichever record ResolveContact finds for
// that name, as a formatted name or a nickname, so a second record
// carrying it could become the operator the next time the name is
// resolved, and every address on it would carry the operator's
// authority. This holds in every turn, the operator's own included; the
// operator adds such a record through CardDAV or the contacts API.
func (t *Tools) ownerNameClaimRefusal(ctx context.Context, args SaveContactArgs, contact *Contact, created bool) error {
	owner := t.legacyOwnerName()
	if owner == "" {
		return nil
	}
	const why = "is the name Thane recognizes the operator by, so another contact that carries it as its name or nickname could take the operator's identity, and the operator's authority on email and Signal with it. Nothing was saved."
	if created {
		if !claimsOwnerName(owner, args.Name, args.Nickname) {
			return nil
		}
		return fmt.Errorf("contact_save refused to create %q: %q %s If this is the operator, contact_owner returns their contact; save to it by its exact name. If it is someone else, save them under a fuller name without that nickname, or ask the operator to add the contact through CardDAV or the contacts API", args.Name, owner, why)
	}
	// Whether the record still answers to the owner name after the save
	// is decided with the resolver's own key: both sides trimmed of edge
	// space and folded as LOWER folds them. So a formatted name stored
	// with edge space still answers, as the resolver finds it, and a
	// Unicode case variant does not pass for the name.
	ownerKey := nameKey(owner)
	stillAnswers := nameKey(args.Nickname) == ownerKey || nameKey(contact.FormattedName) == ownerKey
	if claimsOwnerName(owner, contact.Nickname) && strings.TrimSpace(args.Nickname) != "" && !stillAnswers {
		return t.ownerNicknameReplacementRefusal(ctx, args, contact, owner)
	}
	if !claimsOwnerName(owner, args.Nickname) || sqliteLowerEqual(contact.Nickname, owner) {
		return nil
	}
	operatorID, err := t.custodyOperatorID(ctx)
	if err != nil {
		return err
	}
	if operatorID != uuid.Nil && contact.ID == operatorID {
		return nil
	}
	return fmt.Errorf("contact_save refused nickname %q for %s: %q %s Retry without the nickname, or ask the operator to set it through CardDAV or the contacts API", args.Nickname, contact.FormattedName, owner, why)
}

// ownerNicknameReplacementRefusal refuses, in every turn, replacing the
// nickname through which the operator's own contact answers to the
// legacy owner name. The operator is re-resolved by that name at the
// next start, so without the nickname nobody would be the operator.
func (t *Tools) ownerNicknameReplacementRefusal(ctx context.Context, args SaveContactArgs, contact *Contact, owner string) error {
	operatorID, err := t.custodyOperatorID(ctx)
	if err != nil {
		return err
	}
	if operatorID == uuid.Nil || contact.ID != operatorID {
		return nil
	}
	return fmt.Errorf("contact_save refused nickname %q for %s: its nickname %q is the name Thane recognizes the operator by, and the operator's own contact answers to it only through that nickname, so replacing it would leave Thane unable to find the operator the next time it starts. Nothing was saved. Retry without the nickname; the operator changes it through CardDAV or the contacts API",
		echoForRefusal(args.Nickname), echoForRefusal(contact.FormattedName), owner)
}

// refusalClasses records which kinds of value a refusal lists and which
// rules refused them, so the renderer explains and recovers only what
// applies.
type refusalClasses struct {
	addresses, routing, names bool
	target, addressHeld, name bool
	// shortName marks a name claim refused because a contact with
	// authority answers to the name by a short form, a rule the
	// operator's own message lifts.
	shortName bool
}

func classifyRefusal(violations []IdentityViolation) refusalClasses {
	var c refusalClasses
	for _, v := range violations {
		_, routing := routingPropertyFor(v.Property)
		claim := isClaimProperty(v.Property)
		switch {
		case claim:
			c.names = true
		case routing:
			c.routing = true
		default:
			c.addresses = true
		}
		switch {
		case v.Reason != IdentityReasonHolder:
			c.target = true
		case claim && isShortFormHolder(v.Holder):
			c.shortName = true
		case claim:
			c.name = true
		default:
			c.addressHeld = true
		}
	}
	return c
}

// identityRefusal renders a contact_save custody refusal as teaching:
// why each class of refused value is custody, every refused value with
// its reason within the refusal list budget, that nothing was saved,
// and the recovery that fits the rule each broke. A name refusal fires
// in the operator's own message too, so its recovery never claims the
// turn is unattended. created says whether the save would have created
// the contact, which decides whether a fuller name is a recovery: on an
// existing contact a different name creates a second one.
func identityRefusal(targetName, targetZone string, created bool, violations []IdentityViolation) error {
	c := classifyRefusal(violations)
	noun := "fact(s)"
	if c.names {
		noun = "change(s)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "contact_save refused %d %s; nothing was saved.", len(violations), noun)
	if c.addresses {
		b.WriteString(" Addresses and numbers are how email and Signal recognize a contact and what the send gate trusts.")
	}
	if c.routing {
		b.WriteString(" ha_companion_app is the Home Assistant device that receives a contact's notifications and answers their decision requests, and notification_preference picks the channel they arrive on.")
	}
	if c.names {
		b.WriteString(" A name, nickname or given name is what notifications, decision requests, lookups and conversation context find a contact by.")
	}
	b.WriteString(" They are operator-custodied where a change would move authority:")
	items := make([]string, 0, len(violations))
	for _, v := range violations {
		items = append(items, fmt.Sprintf("%q=%q (%s): %s", echoForRefusal(v.Key), echoForRefusal(v.Value), v.Property, violationReason(v, targetName, targetZone)))
	}
	b.WriteString(boundedRefusalList(items))
	b.WriteString("\n\n")
	switch {
	case c.target && (c.addresses || c.routing):
		b.WriteString("This turn is not the operator's own message, so ask the operator to add these through CardDAV or the contacts API, or to ask you for them in their own message. If a value belongs to someone else, save it on that person's own contact. ")
	case c.target:
		b.WriteString("This turn is not the operator's own message, so ask the operator to change it through CardDAV or the contacts API, or to ask you for it in their own message. ")
	}
	if c.addressHeld {
		b.WriteString("A value an admin, household, trusted or operator contact already holds stays with that contact; if it now belongs to someone else, ask the operator to move it through CardDAV or the contacts API. ")
	}
	switch {
	case c.name && created:
		b.WriteString("A name or nickname an admin, household, trusted or operator contact goes by stays with that contact, in every turn: save this contact under a fuller name or without the nickname, or, if you meant that person, save to their contact by its exact name. ")
	case c.name:
		b.WriteString("A name or nickname an admin, household, trusted or operator contact goes by stays with that contact, in every turn: retry without the nickname, or with one no admin, household, trusted or operator contact goes by. ")
	}
	// Only a turn that is not the operator's own reaches the short-form
	// rule, so this recovery may say so.
	switch {
	case c.shortName && created:
		b.WriteString("A given name or first word an admin, household, trusted or operator contact answers to stays with that contact outside the operator's own message, and this turn is not one: save this contact under a fuller name (\"Bob Jones\", not \"Bob\") or without the nickname, or, if you meant that person, save to their contact by its exact name. If the operator wants this contact to go by it, ask them to say so in their own message, or to set it through CardDAV or the contacts API. ")
	case c.shortName:
		b.WriteString("A given name or first word an admin, household, trusted or operator contact answers to stays with that contact outside the operator's own message, and this turn is not one: retry without the nickname, or with one no admin, household, trusted or operator contact goes by or answers to. If the operator wants this contact to go by it, ask them to say so in their own message, or to set it through CardDAV or the contacts API. ")
	}
	if c.names {
		b.WriteString("Retry without the refused values to save the rest")
	} else {
		b.WriteString("Retry without the refused facts to save the rest")
	}
	return errors.New(b.String())
}

// violationReason is the one-clause reason for a refused value. Stored
// names and values are clipped the way caller-supplied ones are, so
// each clause stays bounded.
func violationReason(v IdentityViolation, targetName, targetZone string) string {
	targetName = echoForRefusal(targetName)
	switch v.Reason {
	case IdentityReasonOperator:
		return fmt.Sprintf("%s is the operator's own contact", targetName)
	case IdentityReasonZone:
		if !ValidTrustZones[targetZone] {
			return fmt.Sprintf("%s has trust zone %q, which is not known", targetName, echoForRefusal(targetZone))
		}
		return fmt.Sprintf("%s is %s", targetName, targetZone)
	}
	h := v.Holder
	if h == nil {
		return "already held by a contact with authority"
	}
	operator := ""
	if h.Operator {
		operator = ", the operator's own contact"
	}
	name := echoForRefusal(h.Name)
	if isClaimProperty(v.Property) {
		switch h.Property {
		case claimGivenName, claimFNFirstWord:
			how := "by its given name"
			if h.Property == claimFNFirstWord {
				how = "as the first word of its formatted name"
			}
			return fmt.Sprintf("%s answers to %q %s (%s, %s%s); a contact with it as its formatted name or nickname would be found by that name instead of %s, and take the notifications, decision requests and context meant for %s",
				name, echoForRefusal(h.Value), how, echoForRefusal(h.Zone), h.ID, operator, name, name)
		}
		return fmt.Sprintf("%s already goes by %q (%s, %s%s); a second contact answering to it would take the notifications, decision requests and context meant for %s",
			name, echoForRefusal(h.Value), echoForRefusal(h.Zone), h.ID, operator, name)
	}
	return fmt.Sprintf("already held by %s (%s, %s%s) as %s %s; a second holder would unmatch %s on %s",
		name, echoForRefusal(h.Zone), h.ID, operator, echoForRefusal(h.Property), echoForRefusal(h.Value), name, identityChannel(v.Property, v.Value))
}

// identityChannel names the channel an identity value is recognized on.
func identityChannel(property, value string) string {
	switch {
	case property == identityEmail:
		return "email"
	case property == identityTel:
		return "Signal"
	case strings.HasPrefix(strings.ToLower(value), "signal:"):
		return "Signal"
	}
	return "its channel"
}

// logIdentityRefusal records one refusal for the operator, keyed to the
// turn that asked for it.
// A dry-run import logs its drops too, marked dry_run, so a preview is
// never mistaken for values an import actually left out.
func logIdentityRefusal(tool string, target uuid.UUID, zone string, violations []IdentityViolation, provenance *PropertyProvenance, dryRun bool) {
	rules := map[string]struct{}{}
	properties := map[string]struct{}{}
	holders := map[string]struct{}{}
	for _, v := range violations {
		rules[v.Reason] = struct{}{}
		properties[v.Property] = struct{}{}
		if v.Holder != nil {
			holders[v.Holder.ID.String()] = struct{}{}
		}
	}
	contactID := ""
	if target != uuid.Nil {
		contactID = target.String()
	}
	if provenance == nil {
		provenance = &PropertyProvenance{}
	}
	slog.Warn("contact identity custody refused",
		"tool", tool,
		"contact_id", contactID,
		"zone", zone,
		"rule", strings.Join(sortedKeys(rules), ","),
		"properties", strings.Join(sortedKeys(properties), ","),
		"holder_id", strings.Join(sortedKeys(holders), ","),
		"dry_run", dryRun,
		"request_id", provenance.RequestID,
		"conversation_id", provenance.ConversationID,
		"loop_id", provenance.LoopID)
}
