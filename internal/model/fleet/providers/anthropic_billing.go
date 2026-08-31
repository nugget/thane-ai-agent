package providers

import (
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// billingProbeInterval is how often one real request is let through
// while the account is billing-blocked. The old behavior retried on
// every loop iteration — which is also how recovery was detected — so
// the probe keeps recovery detection within a minute of a credit top-up
// while everything between probes fails fast without an HTTP call.
const billingProbeInterval = time.Minute

// billingBlockedMarker identifies Anthropic's credit-balance refusal in
// a 400 body ("Your credit balance is too low to access the Anthropic
// API. ..."). Matched case-insensitively and deliberately narrow: only
// this shape is a billing state; every other 400 stays an ordinary
// request error.
const billingBlockedMarker = "credit balance"

// BillingSnapshot describes the account's billing-blocked state for
// observability surfaces. Nil (from the accessor) means not blocked.
type BillingSnapshot struct {
	Blocked bool
	Since   time.Time
	Detail  string
}

// anthropicBilling holds the billing-blocked state for one client. Its
// design mirrors the calendar snapshot's sharing-disabled state one
// layer down: a persistent, operator-actionable refusal is state with
// transition edges, not a fault to rediscover per call.
type anthropicBilling struct {
	mu      sync.Mutex
	blocked bool
	since   time.Time
	detail  string
	retryAt time.Time
	hook    func(blocked bool, detail string)
}

// SetBillingTransitionHook registers fn to be called on billing-state
// edges (blocked=true on entry, false on recovery). Called at most once
// per edge, synchronously from the request path — the hook must be
// quick and must not call back into the client; spawn a goroutine for
// real work.
func (c *AnthropicClient) SetBillingTransitionHook(fn func(blocked bool, detail string)) {
	if c == nil {
		return
	}
	c.billing.mu.Lock()
	c.billing.hook = fn
	c.billing.mu.Unlock()
}

// BillingSnapshot returns the current billing-blocked state, or nil
// when the account is not blocked. Safe for concurrent use.
func (c *AnthropicClient) BillingSnapshot() *BillingSnapshot {
	if c == nil {
		return nil
	}
	c.billing.mu.Lock()
	defer c.billing.mu.Unlock()
	if !c.billing.blocked {
		return nil
	}
	return &BillingSnapshot{Blocked: true, Since: c.billing.since, Detail: c.billing.detail}
}

// billingFastFail returns the standing billing refusal without an HTTP
// round-trip while the account is blocked, letting one probe through
// per billingProbeInterval so recovery is still discovered. Nil when
// the call should proceed.
func (c *AnthropicClient) billingFastFail() error {
	c.billing.mu.Lock()
	defer c.billing.mu.Unlock()
	if !c.billing.blocked {
		return nil
	}
	now := time.Now()
	if !now.Before(c.billing.retryAt) {
		c.billing.retryAt = now.Add(billingProbeInterval)
		return nil
	}
	return &llm.APIError{Provider: "anthropic", StatusCode: 400, Body: c.billing.detail, Billing: true}
}

// noteBillingRefusal records a billing 400 and reports whether this is
// the transition edge into the blocked state. The hook fires on the
// edge only.
func (c *AnthropicClient) noteBillingRefusal(body string) bool {
	c.billing.mu.Lock()
	transition := !c.billing.blocked
	if transition {
		c.billing.blocked = true
		c.billing.since = time.Now()
	}
	c.billing.detail = body
	c.billing.retryAt = time.Now().Add(billingProbeInterval)
	hook := c.billing.hook
	c.billing.mu.Unlock()
	if transition && hook != nil {
		hook(true, body)
	}
	return transition
}

// clearBillingBlocked clears the state on a successful response and
// reports whether this is the recovery edge.
func (c *AnthropicClient) clearBillingBlocked() bool {
	c.billing.mu.Lock()
	transition := c.billing.blocked
	detail := c.billing.detail
	if transition {
		c.billing.blocked = false
		c.billing.since = time.Time{}
		c.billing.detail = ""
		c.billing.retryAt = time.Time{}
	}
	hook := c.billing.hook
	c.billing.mu.Unlock()
	if transition && hook != nil {
		hook(false, detail)
	}
	return transition
}

// anthropicBillingBody reports whether an error response encodes the
// account billing state rather than a request problem.
func anthropicBillingBody(status int, body string) bool {
	return status == 400 && strings.Contains(strings.ToLower(body), billingBlockedMarker)
}
