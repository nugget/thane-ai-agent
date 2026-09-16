package email

import (
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	platformconfig "github.com/nugget/thane-ai-agent/internal/platform/config"
)

// The built-in loops mail is routed to. The loop definition runtime
// registers each as a durable built-in whenever email polling is on and
// something routes to it; a core loops/ document with the same name
// overrides it.
const (
	// DefaultHandlerLoopName is the built-in event-driven loop that
	// receives an assistant mailbox's new-mail wakes unless the account
	// names another wake_loop.
	DefaultHandlerLoopName = platformconfig.EmailWakeLoopDefaultHandler

	// OwnerTriageLoopName is the built-in first pass over an operator
	// mailbox, the wake_loop such an account gets by default.
	OwnerTriageLoopName = platformconfig.EmailWakeLoopOwnerTriage

	// DraftReviewLoopName is the built-in review pass, which an account
	// gets only by naming it as its review_loop.
	DraftReviewLoopName = platformconfig.EmailReviewLoopDraftReview
)

// wakeTargetFor returns the loop that receives an account's new-mail
// wakes: the account's mailbox.wake_loop, else its owner's default
// ([OwnerTriageLoopName] for an operator mailbox, [DefaultHandlerLoopName]
// otherwise). Routing is per account because whose mailbox it is decides
// which pass should see its mail first. An account the manager does not
// know is an error rather than a fallback, so the poll leaves its
// high-water mark in place instead of waking the wrong loop.
func (p *Poller) wakeTargetFor(account string) (messages.LoopWakeTarget, error) {
	cfg, err := p.manager.AccountConfig(account)
	if err != nil {
		return messages.LoopWakeTarget{}, fmt.Errorf("route new mail for account %q: %w", account, err)
	}
	return messages.LoopWakeTarget{Name: cfg.WakeLoopName()}, nil
}
