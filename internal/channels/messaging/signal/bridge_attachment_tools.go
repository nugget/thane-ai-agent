package signal

import (
	"context"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// attachmentsTag is the capability tag of the attachment tools:
// attachment_list, attachment_search and attachment_describe.
const attachmentsTag = "attachments"

// grantsAttachmentTools decides whether a turn whose messages carry
// attachments gets the attachment tools, and records the decision in the
// turn summary and on log.
//
// A message line describes an attachment but cannot show it. With the
// tools the model can ask for a description, or learn why there is
// none, instead of having only the line to go on. The tools read every
// stored attachment, not only this conversation's, so they go only to
// the owner and to senders whose trust zone allows broad tool access
// (admin and household). They exist only where the attachment store
// does. The waking messages decide before the turn starts; messages
// merged into the running turn get the same rule through
// [Bridge.grantAttachmentToolsMidTurn]. A granted tag stays active on
// the sender's later turns until the model drops it, like any tag the
// model activates itself.
func (b *Bridge) grantsAttachmentTools(log *slog.Logger, binding *memory.ChannelBinding, attachmentCount int, summary map[string]any) bool {
	if attachmentCount == 0 || b.attachmentStore == nil {
		return false
	}
	zone, owner := contacts.ZoneUnknown, false
	if binding != nil {
		owner = binding.IsOwner
		if binding.TrustZone != "" {
			zone = binding.TrustZone
		}
	}
	if !owner && !broadToolAccess(zone) {
		summary["attachment_tools"] = "withheld"
		log.Info("signal attachment tools withheld",
			"attachments", attachmentCount,
			"trust_zone", zone,
		)
		return false
	}
	summary["attachment_tools"] = "granted"
	log.Info("signal attachment tools granted",
		"attachments", attachmentCount,
		"trust_zone", zone,
		"is_owner", owner,
	)
	return true
}

// grantAttachmentToolsMidTurn applies grantsAttachmentTools to the
// attachments of messages a mid-turn pull delivered and, on a grant,
// activates the tag on the live run through the pull context's
// [agentctx.TagRequester], from the run's next model call. A turn decides
// at most once: when its waking messages or an earlier pull already
// decided, the sender, and so the answer, is unchanged.
func (b *Bridge) grantAttachmentToolsMidTurn(ctx context.Context, scaffold signalTurnScaffold, attachmentCount int, summary map[string]any) {
	if _, decided := summary["attachment_tools"]; decided {
		return
	}
	log := scaffold.log.With("request_id", logging.RequestIDFromContext(ctx), "mid_turn", true)
	granted := b.grantsAttachmentTools(log, scaffold.channelBinding, attachmentCount, summary)
	if _, decided := summary["attachment_tools"]; !decided {
		return // no attachment, or no attachment store: nothing to decide
	}
	summary["attachment_tools_mid_turn"] = true
	if !granted {
		return
	}
	if err := agentctx.RequestTag(ctx, attachmentsTag); err != nil {
		summary["attachment_tools"] = "grant_failed"
		log.Warn("signal attachment tools grant failed",
			"attachments", attachmentCount,
			"error", err,
		)
	}
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
