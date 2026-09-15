package email

import (
	"fmt"
	"slices"
)

// The drafts-only gate. On an account whose delivery is drafts and
// whose draft gate is relaxed, the operator reads and sends every
// message by hand, so their send is the gate for a question the trust
// zones otherwise answer: may this person be written to at all. Two
// rules follow, and both only ever turn a refusal into a draft:
//
//   - A recipient refused only for its zone is drafted, with gating
//     draft_only (relaxForDraftsOnly).
//   - An unattended reply to bulk mail a person sent that names the
//     account's own address in its To or Cc is drafted rather than
//     refused as an automatic response (personallyAddressedListReply).
//
// What no person could usefully answer stays refused: an automated
// mailbox, an address the directory could not judge, a denied or
// not-allowed domain, and an automatic reply, bounce, or notification.
//
// The operator's send being the gate, the draft must show them where it
// goes, so it names a draft_only recipient by bare address
// (withoutDraftOnlyNames).

// draftOnlyClause ends the reason a draft_only recipient carries.
const draftOnlyClause = "drafted only because this account's delivery is drafts with draft_gate relaxed: the operator reads and sends every draft by hand, so nothing reaches this recipient unless they send it"

// relaxForDraftsOnly drafts, on an account whose draft gate is relaxed,
// for every recipient the trust gate refused only because of its zone:
// an unmatched address, a contact at a blocked zone, and an address
// several records share whose least privileged zone is blocked. It must
// run before applyDomainRules, which skips recipients already refused:
// run after it, a stranger at a denied domain would be drafted.
func relaxForDraftsOnly(result *TrustResult, cfg AccountConfig) {
	if !cfg.RelaxedDraftGate() {
		return
	}
	for i := range result.Assessments {
		a := &result.Assessments[i]
		if a.Allowed || !refusedOnlyForZone(*a) {
			continue
		}
		blocked := "Cannot send to " + a.Address + ": " + a.Reason
		if j := slices.Index(result.Blocked, blocked); j >= 0 {
			result.Blocked = slices.Delete(result.Blocked, j, j+1)
		}
		a.Allowed = true
		a.Gating = GatingDraftOnly
		a.Reason = draftOnlyReason(*a)
		result.Allowed = append(result.Allowed, a.Address)
	}
}

// refusedOnlyForZone reports whether the gate refused a recipient for
// its zone and nothing else. An automated mailbox is refused by its
// own name, a failed lookup is no judgement at all, and an address that
// does not parse can be neither looked up nor delivered, so none of
// them qualifies.
func refusedOnlyForZone(a RecipientAssessment) bool {
	if a.Automated {
		return false
	}
	switch a.ContactStatus {
	case ContactUnmatched, ContactMatched, ContactAmbiguous:
	default:
		return false
	}
	if _, err := parseAddress(a.Address); err != nil {
		return false
	}
	return gatingForZone(a.TrustZone) == GatingBlocked
}

// draftOnlyReason says which zone would have refused the recipient and
// why it was drafted instead. An ambiguous address keeps the candidates
// its reason already names.
func draftOnlyReason(a RecipientAssessment) string {
	switch a.ContactStatus {
	case ContactUnmatched:
		return "no contact record; " + draftOnlyClause
	case ContactAmbiguous:
		return a.Reason + "; " + draftOnlyClause
	}
	return fmt.Sprintf("the contact is at the %s trust zone, whose send policy is blocked; %s", a.TrustZone, draftOnlyClause)
}

// withoutDraftOnlyNames returns addrs with every draft_only recipient
// written as its bare address. A display name is whatever the model or
// the original message supplied, and the operator's client shows it in
// place of the address: kept on a recipient the directory does not
// vouch for, "Alice" <mallory@example.net> would read as Alice to the
// operator about to send. A recipient the gate allowed keeps its name.
func withoutDraftOnlyNames(addrs []string, assessments []RecipientAssessment) []string {
	draftOnly := make(map[string]bool)
	for _, a := range assessments {
		if a.Gating == GatingDraftOnly {
			draftOnly[a.Address] = true
		}
	}
	if len(draftOnly) == 0 {
		return addrs
	}
	out := make([]string, len(addrs))
	for i, raw := range addrs {
		out[i] = raw
		if parsed, err := parseAddress(raw); err == nil && draftOnly[parsed.Key()] {
			out[i] = parsed.Address
		}
	}
	return out
}

// personallyAddressedListReply reports whether an unattended reply to a
// marked original may be drafted anyway: the account's draft gate is
// relaxed, the original is bulk mail a person sent (not auto_submitted,
// and not marked by Precedence junk alone, the mark classic
// autoresponders put on their replies), and its own To or Cc names the
// account's address. A person answers list mail addressed to them. Mail
// that reached the account only through a list address, or that a
// machine sent, stays an automatic response.
func personallyAddressedListReply(cfg AccountConfig, original HeaderMarks, originalRecipients []Address) bool {
	if !cfg.RelaxedDraftGate() || !original.Bulk || original.AutoSubmitted != "" || original.junkOnly {
		return false
	}
	own := Address{Address: accountAddress(cfg)}.Key()
	if own == "" {
		return false
	}
	return slices.ContainsFunc(originalRecipients, func(a Address) bool { return a.Key() == own })
}

// listReplyGap is the clause an automatic_response refusal adds on an
// account whose draft gate is relaxed, naming why the list-mail rule
// did not draft this reply. It is empty on every other account, which
// has no such rule.
func listReplyGap(cfg AccountConfig, original HeaderMarks) string {
	if !cfg.RelaxedDraftGate() {
		return ""
	}
	switch {
	case original.AutoSubmitted != "":
		return ", and this drafts-only account drafts such a reply only for list or bulk mail a person sent, never for an automatic reply, bounce, or notification"
	case original.junkOnly:
		return ", and this drafts-only account drafts such a reply only for list or bulk mail a person sent, never for mail whose one bulk mark is Precedence junk, which automatic replies such as out-of-office notices carry"
	}
	return ", and this drafts-only account drafts a reply to list or bulk mail only when the original names this account's own address in its to or cc, which this one does not"
}
