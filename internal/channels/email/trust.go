package email

import (
	"context"
	"fmt"
	"strings"
)

// RecipientAssessment is the gate's verdict on one recipient.
type RecipientAssessment struct {
	Address       string        `json:"address"`
	TrustZone     string        `json:"trust_zone"`
	ContactStatus ContactStatus `json:"contact_status"`
	ContactID     string        `json:"contact_id,omitempty"`
	ContactName   string        `json:"contact_name,omitempty"`

	// Allowed is whether this recipient alone would pass the gate.
	Allowed bool `json:"allowed"`

	// Reason explains a refusal and names the recovery.
	Reason string `json:"reason,omitempty"`
}

// TrustResult is the gate's verdict on a recipient set. A message is
// sent only when every recipient is allowed; the refusal text names
// each recipient at issue and what would fix it.
type TrustResult struct {
	// Allowed lists the recipients that passed, as given.
	Allowed []string

	// Blocked lists one refusal message per recipient that did not.
	Blocked []string

	// Assessments carries the per-recipient detail behind both lists.
	Assessments []RecipientAssessment
}

// CheckRecipientTrust evaluates each address against the contact
// directory. A nil resolver disables gating and allows everything. An
// address that does not parse is blocked: it can neither be looked up
// nor delivered to. A duplicate directory record and a store failure
// are each blocked with their own reason rather than passed off as a
// stranger.
func CheckRecipientTrust(ctx context.Context, resolver ContactResolver, addresses []string) TrustResult {
	var result TrustResult
	if resolver == nil {
		result.Allowed = addresses
		return result
	}

	lookup := newIdentityLookup(ctx, resolver, nil)
	for _, raw := range addresses {
		parsed, err := parseAddress(raw)
		if err != nil {
			result.Blocked = append(result.Blocked, fmt.Sprintf("Cannot send to %q: not a valid email address.", raw))
			result.Assessments = append(result.Assessments, RecipientAssessment{Address: raw, TrustZone: ZoneUnknown, ContactStatus: ContactUnmatched, Reason: "not a valid email address"})
			continue
		}
		assessment := assessRecipient(parsed, lookup.resolve(parsed))
		result.Assessments = append(result.Assessments, assessment)
		if assessment.Allowed {
			result.Allowed = append(result.Allowed, raw)
		} else {
			result.Blocked = append(result.Blocked, "Cannot send to "+assessment.Address+": "+assessment.Reason)
		}
	}
	return result
}

// assessRecipient applies the zone table to one resolved recipient.
func assessRecipient(addr Address, match ContactMatch) RecipientAssessment {
	a := RecipientAssessment{
		Address:       addr.Key(),
		TrustZone:     match.TrustZone,
		ContactStatus: match.Status,
	}
	if match.Binding != nil {
		a.ContactID = match.Binding.ContactID
		a.ContactName = match.Binding.ContactName
	}
	switch match.Status {
	case ContactLookupFailed:
		a.Reason = fmt.Sprintf("the contact directory could not be consulted (%v); this is not a judgement about the recipient, retry later", match.Err)
		return a
	case ContactUnmatched:
		a.Reason = "no contact record; add one deliberately with contact_save or drop the recipient"
		return a
	case ContactAmbiguous:
		names := make([]string, 0, len(match.Candidates))
		for _, c := range match.Candidates {
			names = append(names, fmt.Sprintf("%s (%s, %s)", c.Name, c.ID, c.TrustZone))
		}
		a.Reason = fmt.Sprintf("the address belongs to %d contact records [%s]; the least privileged zone (%s) governs until the duplicates are merged", len(match.Candidates), strings.Join(names, "; "), match.TrustZone)
		if sendEligibleZone(match.TrustZone) {
			a.Allowed = true
		}
		return a
	}

	switch match.TrustZone {
	case "admin", "household", "trusted":
		a.Allowed = true
	case "known":
		a.Reason = "the contact is at the known trust zone, which does not permit sending; promote the contact deliberately (with the operator's authorization) or drop the recipient"
	default:
		a.Reason = fmt.Sprintf("the contact's trust zone %q does not permit sending", match.TrustZone)
	}
	return a
}

// sendEligibleZone reports whether a zone may receive mail from the
// agent under today's gate.
func sendEligibleZone(zone string) bool {
	switch zone {
	case "admin", "household", "trusted":
		return true
	}
	return false
}

// HasIssues reports whether any recipient was blocked.
func (tr TrustResult) HasIssues() bool {
	return len(tr.Blocked) > 0
}

// FormatIssues renders the refusal for the model: every blocked
// recipient with its reason, so one retry can fix all of them.
func (tr TrustResult) FormatIssues() string {
	parts := make([]string, 0, len(tr.Blocked))
	for _, b := range tr.Blocked {
		parts = append(parts, "✗ "+b)
	}
	return fmt.Sprintf("Email not sent — recipient trust issues:\n\n%s", strings.Join(parts, "\n"))
}
