package email

import (
	"context"
	"fmt"
	"slices"

	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// The junk guard keeps a turn the operator is not present for from
// filing mail from people the operator trusts as spam. It only refuses:
// it never moves anything itself, never widens what a move may do, and
// has no say in an attended turn, where the operator may junk anything.
// It judges each message on its own, so one protected sender in a batch
// does not hold back the rest.

// junkProtectedZones are the record zones whose mail an unattended turn
// may not move into junk.
var junkProtectedZones = []string{contacts.ZoneAdmin, contacts.ZoneHousehold, contacts.ZoneTrusted}

// junkRefusalRecovery is the next move for a message the guard refused.
const junkRefusalRecovery = "it stayed where it was: flag it with email_mark flag \"flagged\" if it needs the operator, and bring it to them with request_core_attention if it cannot wait; do not retry the move"

// refusedMessage is one message the junk guard kept out of the junk
// folder: who sent it, why it stays, and what to do instead. An entry
// past the result's size budget keeps only its UID (see
// marshalMoveResponse).
type refusedMessage struct {
	UID       uint32 `json:"uid"`
	From      string `json:"from,omitempty"`
	TrustZone string `json:"trust_zone,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Recovery  string `json:"recovery,omitempty"`
}

// guardJunk splits the UIDs of an unattended move into the junk folder
// into those that may move and those it refuses. A UID with no envelope
// moves on, so the move itself reports it under uids_not_found. Every
// refusal is logged, keyed to the loop and conversation that asked.
func (s *Service) guardJunk(ctx context.Context, acct ResolvedAccount, uids []uint32, senders map[uint32]moveSender) ([]uint32, []refusedMessage) {
	var allowed []uint32
	var refused []refusedMessage
	for _, uid := range uids {
		sender, ok := senders[uid]
		if !ok {
			allowed = append(allowed, uid)
			continue
		}
		reason := junkProtection(sender.match)
		if reason == "" {
			allowed = append(allowed, uid)
			continue
		}
		zone := judgedZone(sender.match)
		refused = append(refused, refusedMessage{
			UID:       uid,
			From:      sender.envelope.From.String(),
			TrustZone: zone,
			Reason:    reason,
			Recovery:  junkRefusalRecovery,
		})
		s.logger.Info("email junk move refused",
			"account", acct.Name,
			"uid", uid,
			"message_id", sender.envelope.MessageID,
			"from_address", sender.envelope.From.Key(),
			"contact_status", string(sender.match.Status),
			"trust_zone", zone,
			"reason", reason,
			"loop_id", tools.LoopIDFromContext(ctx),
			"conversation_id", tools.ConversationIDFromContext(ctx),
		)
	}
	return allowed, refused
}

// junkProtection says why an unattended turn may not junk mail from a
// sender, or returns "" when it may. The zone read is the record's own,
// not the automated cap: a notice address the operator filed at
// trusted is still one they chose to trust. A directory that could not
// answer protects too, because the guard must not guess in the
// direction that loses mail.
func junkProtection(match ContactMatch) string {
	switch match.Status {
	case ContactLookupFailed:
		return "the contact directory could not be consulted, so the sender may be someone the operator trusts"
	case ContactMatched:
		if match.Binding == nil {
			return ""
		}
		if match.Binding.IsOwner {
			return "the address belongs to the operator's own contact record (is_owner)"
		}
		if zone := judgedZone(match); slices.Contains(junkProtectedZones, zone) {
			return fmt.Sprintf("a contact at %s holds the address", zone)
		}
	case ContactAmbiguous:
		if slices.Contains(junkProtectedZones, match.TrustZone) {
			return fmt.Sprintf("several contacts hold the address, all of them at %s or above", match.TrustZone)
		}
		for _, c := range match.Candidates {
			if slices.Contains(junkProtectedZones, c.TrustZone) {
				return fmt.Sprintf("several contacts hold the address, one of them at %s", c.TrustZone)
			}
		}
		if match.CandidatesTotal > len(match.Candidates) {
			// The directory named only some of the records; one it left
			// out may be protected, and the effective zone is the least
			// privileged, so it cannot say.
			return fmt.Sprintf("%d contacts hold the address and not all of them could be checked, so one may be someone the operator trusts", match.CandidatesTotal)
		}
	}
	return ""
}

// judgedZone is the zone the guard judges a sender at, and the one a
// refusal reports: a matched record's own zone, before the automated
// cap lowers it, and otherwise the effective zone.
func judgedZone(match ContactMatch) string {
	if match.Status == ContactMatched && match.Binding != nil && match.Binding.TrustZone != "" {
		return match.Binding.TrustZone
	}
	return match.TrustZone
}
