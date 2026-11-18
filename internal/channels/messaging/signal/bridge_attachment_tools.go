package signal

import "github.com/nugget/thane-ai-agent/internal/state/contacts"

// attachmentsTag is the capability tag of the attachment tools:
// attachment_list, attachment_search and attachment_describe.
const attachmentsTag = "attachments"

// grantsAttachmentTools decides whether a turn whose waking messages
// carry attachments starts with the attachment tools active, and
// records the decision in the turn summary and the log. Only the
// messages that start the turn count: a message merged in while the
// turn runs cannot change its tags, though the model can still activate
// the tag itself.
//
// A message line describes an attachment but cannot show it. With the
// tools the model can ask for a description, or learn why there is
// none, instead of having only the line to go on. The tools read every
// stored attachment, not only this conversation's, so they go only to
// the owner and to senders whose trust zone allows broad tool access
// (admin and household). They exist only where the attachment store
// does. A granted tag stays active on the sender's later turns until the
// model drops it, like any tag the model activates itself.
func (b *Bridge) grantsAttachmentTools(scaffold signalTurnScaffold, attachmentCount int, summary map[string]any) bool {
	if attachmentCount == 0 || b.attachmentStore == nil {
		return false
	}
	zone, owner := contacts.ZoneUnknown, false
	if binding := scaffold.channelBinding; binding != nil {
		owner = binding.IsOwner
		if binding.TrustZone != "" {
			zone = binding.TrustZone
		}
	}
	if !owner && !broadToolAccess(zone) {
		summary["attachment_tools"] = "withheld"
		scaffold.log.Info("signal attachment tools withheld",
			"attachments", attachmentCount,
			"trust_zone", zone,
		)
		return false
	}
	summary["attachment_tools"] = "granted"
	scaffold.log.Info("signal attachment tools granted",
		"attachments", attachmentCount,
		"trust_zone", zone,
		"is_owner", owner,
	)
	return true
}

// broadToolAccess reports whether a trust zone's policy gives its
// senders tools beyond a safe subset.
func broadToolAccess(zone string) bool {
	switch contacts.Policy(zone).ToolAccess {
	case "unrestricted", "most":
		return true
	}
	return false
}
