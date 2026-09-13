package email

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// RecipientAssessment is the gate's verdict on one recipient.
type RecipientAssessment struct {
	Address       string        `json:"address"`
	TrustZone     string        `json:"trust_zone"`
	ContactStatus ContactStatus `json:"contact_status"`

	// Contact is the matched record in the same shape an address view
	// carries, or null when the status is anything but matched.
	Contact *contactView `json:"contact"`

	// Gating is the send policy of the effective zone: allowed,
	// confirmation (held in Drafts under by_trust_zone delivery), or
	// blocked.
	Gating string `json:"gating"`

	// Allowed is whether this recipient alone would pass the gate: its
	// gating is not blocked, so the message may leave as sent or as a
	// draft.
	Allowed bool `json:"allowed"`

	// Reason explains a refusal and names the recovery. On an allowed
	// ambiguous address it says which zone governed.
	Reason string `json:"reason,omitempty"`
}

// TrustResult is the gate's verdict on a recipient set. A message is
// sent only when every recipient is allowed; the refusal text names
// each recipient at issue and what would fix it.
type TrustResult struct {
	// Allowed lists the recipients that passed, as given.
	Allowed []string

	// Blocked lists one "Cannot send to <address>: <reason>" line per
	// recipient that did not pass, for logs and tests; the model reads
	// the assessments.
	Blocked []string

	// Assessments carries the per-recipient detail behind both lists.
	Assessments []RecipientAssessment
}

// CheckRecipientTrust evaluates each address against the contact
// directory. A nil resolver disables contact gating: every parseable
// address is allowed as an unmatched stranger, but each still gets an
// assessment, so the result says who was assessed. An address that does
// not parse is blocked: it can neither be looked up nor delivered to. A duplicate directory record and a store failure
// are each blocked with their own reason rather than passed off as a
// stranger.
func CheckRecipientTrust(ctx context.Context, resolver ContactResolver, addresses []string) TrustResult {
	var result TrustResult
	lookup := newIdentityLookup(ctx, resolver, nil)
	for _, raw := range addresses {
		parsed, err := parseAddress(raw)
		if err != nil {
			result.Blocked = append(result.Blocked, fmt.Sprintf("Cannot send to %q: not a valid email address.", raw))
			result.Assessments = append(result.Assessments, RecipientAssessment{Address: raw, TrustZone: ZoneUnknown, ContactStatus: ContactUnmatched, Gating: GatingBlocked, Reason: "not a valid email address"})
			continue
		}
		var assessment RecipientAssessment
		if resolver == nil {
			assessment = RecipientAssessment{Address: parsed.Key(), TrustZone: ZoneUnknown, ContactStatus: ContactUnmatched, Gating: GatingAllowed, Allowed: true}
		} else {
			assessment = assessRecipient(parsed, lookup.resolve(parsed))
		}
		result.Assessments = append(result.Assessments, assessment)
		if assessment.Allowed {
			result.Allowed = append(result.Allowed, raw)
		} else {
			result.Blocked = append(result.Blocked, "Cannot send to "+assessment.Address+": "+assessment.Reason)
		}
	}
	return result
}

// assessRecipient applies the contacts package's per-zone send policy
// to one resolved recipient. Only a matched or ambiguous address has a
// zone to read; a stranger and a failed lookup are blocked with their
// own reasons, never passed off as a zone.
func assessRecipient(addr Address, match ContactMatch) RecipientAssessment {
	a := RecipientAssessment{
		Address:       addr.Key(),
		TrustZone:     match.TrustZone,
		ContactStatus: match.Status,
		Gating:        GatingBlocked,
	}
	if match.Binding != nil {
		a.Contact = &contactView{ID: match.Binding.ContactID, Name: match.Binding.ContactName, IsOwner: match.Binding.IsOwner}
	}
	switch match.Status {
	case ContactLookupFailed:
		a.Reason = fmt.Sprintf("the contact directory could not be consulted (%v); this is not a judgement about the recipient, retry later", match.Err)
		return a
	case ContactUnmatched:
		a.Reason = "no contact record; add one deliberately with contact_save or drop the recipient"
		return a
	case ContactAmbiguous:
		a.Gating = gatingForZone(match.TrustZone)
		a.Allowed = a.Gating != GatingBlocked
		names := make([]string, 0, len(match.Candidates))
		for _, c := range match.Candidates {
			names = append(names, fmt.Sprintf("%s (%s, %s)", c.Name, c.ID, c.TrustZone))
		}
		a.Reason = fmt.Sprintf("the address belongs to %d contact records [%s]; the least privileged zone (%s) governs until the duplicates are merged", len(match.Candidates), strings.Join(names, "; "), match.TrustZone)
		return a
	}

	a.Gating = gatingForZone(match.TrustZone)
	a.Allowed = a.Gating != GatingBlocked
	if !a.Allowed {
		a.Reason = fmt.Sprintf("the contact is at the %s trust zone, whose send policy is blocked; promote the contact deliberately (with the operator's authorization) or drop the recipient", match.TrustZone)
	}
	return a
}

// HasIssues reports whether any recipient was refused. It reads the
// assessments, which are the model-facing record; Blocked is a
// convenience list of the same refusals for logs and tests.
func (tr TrustResult) HasIssues() bool {
	return slices.ContainsFunc(tr.Assessments, func(a RecipientAssessment) bool { return !a.Allowed })
}
