package config

import "fmt"

// Draft gates: how the trust gate treats recipients on an account whose
// delivery is drafts, where the operator sends every message by hand.
const (
	// EmailDraftGateRelaxed drafts for a recipient the trust gate refuses
	// only for its zone, because the operator's own send is the gate.
	// Automated mailboxes, failed directory lookups, and the account's
	// recipient-domain rules stay refused. It is the default with
	// delivery drafts and is refused with any other delivery mode.
	EmailDraftGateRelaxed = "relaxed"

	// EmailDraftGateStrict applies the trust gate as every delivery mode
	// does. It is the default with by_trust_zone and direct delivery.
	EmailDraftGateStrict = "strict"
)

// DraftGateMode returns the effective draft gate: the configured value,
// else relaxed when delivery is drafts and strict otherwise.
func (a EmailAccountConfig) DraftGateMode() string {
	if a.Policy.DraftGate != "" {
		return a.Policy.DraftGate
	}
	if a.DeliveryMode() == EmailDeliveryDrafts {
		return EmailDraftGateRelaxed
	}
	return EmailDraftGateStrict
}

// RelaxedDraftGate reports whether the account drafts for recipients
// the trust gate refuses only for their zone: it may write mail, its
// delivery is drafts, and its draft gate is relaxed. Each condition is
// checked here rather than trusted to validation, so a configuration
// that skipped Validate still cannot relax a gate on mail Thane sends.
func (a EmailAccountConfig) RelaxedDraftGate() bool {
	return a.CanDraft() && a.DeliveryMode() == EmailDeliveryDrafts && a.DraftGateMode() == EmailDraftGateRelaxed
}

// validateDraftGate checks policy.draft_gate: a known value, and
// relaxed only beside delivery drafts, because on any other mode a
// recipient the gate relaxed could be sent to.
func (a EmailAccountConfig) validateDraftGate(i int) error {
	switch a.Policy.DraftGate {
	case "", EmailDraftGateStrict:
		return nil
	case EmailDraftGateRelaxed:
		if a.DeliveryMode() != EmailDeliveryDrafts {
			return fmt.Errorf("email.accounts[%d] (%s): policy.draft_gate relaxed needs policy.delivery drafts, where the operator sends every message by hand; with delivery %s it would let Thane send to recipients the trust gate refuses, so set draft_gate: strict or delivery: drafts", i, a.Name, a.DeliveryMode())
		}
		return nil
	}
	return fmt.Errorf("email.accounts[%d] (%s): policy.draft_gate %q is not one of relaxed, strict", i, a.Name, a.Policy.DraftGate)
}
