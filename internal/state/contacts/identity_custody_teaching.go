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
// is configured; the sole-admin fallback is already custodied by its
// zone. A resolution failure other than not-found, an ended ctx
// included, fails closed.
func (t *Tools) custodyOperatorID(ctx context.Context) (uuid.UUID, error) {
	if t.operatorContactID != uuid.Nil {
		return t.operatorContactID, nil
	}
	if t.legacyOperatorPinned {
		return t.legacyOperatorID, nil
	}
	name := strings.TrimSpace(t.ownerContactName)
	if name == "" {
		return uuid.Nil, nil
	}
	operator, err := t.store.resolveContact(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("check identity custody: resolve the configured operator contact: %w", err)
	}
	return operator.ID, nil
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
// compared the way ResolveContact compares a name or nickname.
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
// that name, exact name first and then nickname, so a second record
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
	if !claimsOwnerName(owner, args.Nickname) || claimsOwnerName(owner, contact.Nickname) {
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

// hasIdentityProperty reports whether any property is an identity
// property, so the operator lookup runs only when custody can apply.
func hasIdentityProperty(props []Property) bool {
	for _, p := range props {
		if isIdentityProperty(p.Property) {
			return true
		}
	}
	return false
}

// identityRefusal renders a contact_save identity refusal as teaching:
// every refused fact with its reason, within the refusal list budget,
// that nothing was saved, and the recovery that fits the turn.
func identityRefusal(targetName, targetZone string, violations []IdentityViolation) error {
	var b strings.Builder
	fmt.Fprintf(&b, "contact_save refused %d fact(s); nothing was saved. Addresses and numbers are how email and Signal recognize a contact and what the send gate trusts, so they are operator-custodied where adding one would move authority:", len(violations))
	targetRefused, holderRefused := false, false
	items := make([]string, 0, len(violations))
	for _, v := range violations {
		items = append(items, fmt.Sprintf("%q=%q (%s): %s", echoForRefusal(v.Key), echoForRefusal(v.Value), v.Property, violationReason(v, targetName, targetZone)))
		if v.Reason == IdentityReasonHolder {
			holderRefused = true
		} else {
			targetRefused = true
		}
	}
	b.WriteString(boundedRefusalList(items))
	b.WriteString("\n\n")
	if targetRefused {
		b.WriteString("This turn is not the operator's own message, so ask the operator to add these through CardDAV or the contacts API. If a value belongs to someone else, save it on that person's own contact. ")
	}
	if holderRefused {
		b.WriteString("A value an admin, household, trusted or operator contact already holds stays with that contact; if it now belongs to someone else, ask the operator to move it through CardDAV or the contacts API. ")
	}
	b.WriteString("Retry without the refused facts to save the rest")
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
