package config

import (
	"fmt"
	"strings"
	"time"
)

// Mail routing: which loop a new-mail wake from an account reaches, and
// which loop, if any, reviews that account's mail afterwards. The names
// here are the built-in loops; an operator may name any event_driven
// loop definition instead, and a core loops/ document with the same
// name overrides the built-in.
const (
	// EmailWakeLoopOwnerTriage is the built-in first pass for an
	// operator mailbox, and the wake_loop it gets by default.
	EmailWakeLoopOwnerTriage = "email-owner-triage"

	// EmailWakeLoopDefaultHandler is the built-in handler every other
	// mailbox wakes by default.
	EmailWakeLoopDefaultHandler = "email-default-handler"

	// EmailReviewLoopDraftReview is the built-in review pass an account
	// gets only by naming it as its review_loop.
	EmailReviewLoopDraftReview = "email-draft-review"
)

// Review wake defaults.
const (
	// defaultEmailReviewDelay is how long review work gathers before the
	// review loop is woken.
	defaultEmailReviewDelay = 15 * time.Minute

	// defaultEmailReviewMaxWait bounds how long arrivals can postpone
	// that wake.
	defaultEmailReviewMaxWait = 2 * time.Hour
)

// DefaultWakeLoopName returns the wake loop the account's owner gets
// when mailbox.wake_loop is unset.
func (a EmailAccountConfig) DefaultWakeLoopName() string {
	if a.OperatorMailbox() {
		return EmailWakeLoopOwnerTriage
	}
	return EmailWakeLoopDefaultHandler
}

// WakeLoopName returns the loop that receives the account's new-mail
// wakes: mailbox.wake_loop, trimmed, or the owner's default.
func (a EmailAccountConfig) WakeLoopName() string {
	if name := strings.TrimSpace(a.Mailbox.WakeLoop); name != "" {
		return name
	}
	return a.DefaultWakeLoopName()
}

// ReviewLoopName returns the loop that reviews the account's mail, or
// "" when the account has no review pass.
func (a EmailAccountConfig) ReviewLoopName() string {
	return strings.TrimSpace(a.Mailbox.ReviewLoop)
}

// ReviewDelay returns how long review work gathers before the review
// loop is woken, applying the default when unset.
func (a EmailAccountConfig) ReviewDelay() time.Duration {
	if a.Mailbox.ReviewDelay != 0 {
		return a.Mailbox.ReviewDelay
	}
	return defaultEmailReviewDelay
}

// ReviewMaxWait returns how long arrivals may postpone the review wake,
// applying the default when unset.
func (a EmailAccountConfig) ReviewMaxWait() time.Duration {
	if a.Mailbox.ReviewMaxWait != 0 {
		return a.Mailbox.ReviewMaxWait
	}
	return defaultEmailReviewMaxWait
}

// applyRoutingDefaults fills the routing fields with their effective
// values.
func (a *EmailAccountConfig) applyRoutingDefaults() {
	a.Mailbox.WakeLoop = a.WakeLoopName()
	a.Mailbox.ReviewLoop = a.ReviewLoopName()
	a.Mailbox.ReviewDelay = a.ReviewDelay()
	a.Mailbox.ReviewMaxWait = a.ReviewMaxWait()
}

// validateRouting checks the account's wake and review routing. Whether
// each named loop exists, and is event_driven, can only be checked once
// the loop definitions are known; the app does that at hydration.
func (a EmailAccountConfig) validateRouting(i int) error {
	if a.Mailbox.ReviewDelay < 0 {
		return fmt.Errorf("email.accounts[%d] (%s): mailbox.review_delay %s is negative; give a duration such as 15m, or leave it unset for the default", i, a.Name, a.Mailbox.ReviewDelay)
	}
	if a.Mailbox.ReviewMaxWait < 0 {
		return fmt.Errorf("email.accounts[%d] (%s): mailbox.review_max_wait %s is negative; give a duration such as 2h, or leave it unset for the default", i, a.Name, a.Mailbox.ReviewMaxWait)
	}
	if a.ReviewMaxWait() < a.ReviewDelay() {
		return fmt.Errorf("email.accounts[%d] (%s): mailbox.review_max_wait %s is shorter than review_delay %s; the wait bound must be at least the delay it bounds", i, a.Name, a.ReviewMaxWait(), a.ReviewDelay())
	}
	if review := a.ReviewLoopName(); review != "" && review == a.WakeLoopName() {
		return fmt.Errorf("email.accounts[%d] (%s): mailbox.review_loop %q is also the account's wake_loop; the review pass must be a different loop from the one that writes the first drafts, or it would review its own work", i, a.Name, review)
	}
	return nil
}
