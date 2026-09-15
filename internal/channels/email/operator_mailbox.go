package email

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/tools"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// Whose mailbox an account is (mailbox.owner) changes two things here:
// what a read does to the seen flag when the call does not say, and
// whether a turn the operator is not present for may set that flag at
// all. Unread is how the operator sees what is new in their own inbox,
// so a wake that marked their mail seen would hide it from them.

// readMarksSeen returns whether email_read marks the message seen: the
// mark_seen argument when the call passed one, else the account's
// default.
func readMarksSeen(args map[string]any, cfg AccountConfig) bool {
	return toolargs.BoolOr(args, "mark_seen", cfg.ReadMarksSeenByDefault())
}

// refuseUnattendedSeen returns the refusal for setting the seen flag on
// an operator mailbox in a turn the operator is not present for, or nil
// when the change is allowed. nextMove finishes the sentence with what
// the model should do instead. Every refusal is logged, keyed to the
// loop and conversation that asked.
func (s *Service) refuseUnattendedSeen(ctx context.Context, tool string, acct ResolvedAccount, nextMove string) error {
	if !acct.Config.OperatorMailbox() || attended(ctx) {
		return nil
	}
	s.logger.Info("email seen change refused",
		"tool", tool,
		"account", acct.Name,
		"reason", "operator_mailbox_unattended",
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
	return fmt.Errorf("%s cannot mark mail seen on account %q: it is the operator's own mailbox (owner: operator), where unread is how the operator sees what is new, and the operator is not present for this turn; %s", tool, acct.Name, nextMove)
}
