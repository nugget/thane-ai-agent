package providers

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// billingProbeInterval is the recovery-detection cadence while the
// account is billing-blocked: the fast-fail gate lets the first caller
// after each window through, and the active probe below fires on the
// same cadence regardless of traffic. The old behavior retried on
// every loop iteration — which is also how recovery was detected — and
// a billing outage drives every loop into exponential backoff (sleep
// caps reach hours), so without the active probe a top-up on a quiet
// deployment could go undiscovered far past the promised minute.
const billingProbeInterval = time.Minute

// billingProbeTimeout bounds one active probe request.
const billingProbeTimeout = 30 * time.Second

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
	probing bool // an active probe goroutine is running
	hook    func(blocked bool, detail string)
}

// SetBillingTransitionHook registers fn to be called on billing-state
// edges (blocked=true on entry, false on recovery). It is invoked under
// the client's internal lock so edges are delivered in the order they
// were decided — a recovery can never be observed before the block it
// recovers from. The hook therefore must be non-blocking and must not
// call back into the client; hand real work to a queue.
func (c *AnthropicClient) SetBillingTransitionHook(fn func(blocked bool, detail string)) {
	if c == nil {
		return
	}
	c.billing.mu.Lock()
	c.billing.hook = fn
	c.billing.mu.Unlock()
}

// anthropicErrorMessage extracts the human sentence from Anthropic's
// error envelope ({"type":"error","error":{"message":…}}), falling back
// to the raw body when the shape is unfamiliar. The raw body stays on
// APIError for byte-identical error text; the extracted sentence is
// what operators read in the annunciator and the core wake.
func anthropicErrorMessage(body string) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err == nil {
		if msg := strings.TrimSpace(envelope.Error.Message); msg != "" {
			return msg
		}
	}
	return strings.TrimSpace(body)
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
// edge only, under the lock, so edge delivery order matches edge
// decision order.
func (c *AnthropicClient) noteBillingRefusal(body string) bool {
	detail := anthropicErrorMessage(body)
	c.billing.mu.Lock()
	transition := !c.billing.blocked
	if transition {
		c.billing.blocked = true
		c.billing.since = time.Now()
	}
	c.billing.detail = detail
	c.billing.retryAt = time.Now().Add(billingProbeInterval)
	if transition && c.billing.hook != nil {
		c.billing.hook(true, detail)
	}
	startProbe := transition && !c.billing.probing
	if startProbe {
		c.billing.probing = true
	}
	c.billing.mu.Unlock()
	if startProbe {
		go c.billingProbeLoop()
	}
	return transition
}

// billingProbeLoop actively probes for recovery while the account is
// blocked, independent of caller traffic — the outage itself drives
// every loop into backoff, so waiting for a caller could stretch
// recovery detection to the longest sleep cap. Each probe is a 1-token
// Ping, which the billing wall refuses for free; Ping itself notes or
// clears the billing state. The goroutine exits with the blocked state.
func (c *AnthropicClient) billingProbeLoop() {
	defer func() {
		c.billing.mu.Lock()
		c.billing.probing = false
		c.billing.mu.Unlock()
	}()
	ticker := time.NewTicker(billingProbeInterval)
	defer ticker.Stop()
	for range ticker.C {
		if c.BillingSnapshot() == nil {
			return // cleared by regular traffic
		}
		ctx, cancel := context.WithTimeout(context.Background(), billingProbeTimeout)
		_ = c.Ping(ctx)
		cancel()
		if c.BillingSnapshot() == nil {
			return
		}
	}
}

// clearBillingBlocked clears the state on a successful response and
// reports whether this is the recovery edge. Hook ordering as in
// noteBillingRefusal.
func (c *AnthropicClient) clearBillingBlocked() bool {
	c.billing.mu.Lock()
	defer c.billing.mu.Unlock()
	transition := c.billing.blocked
	detail := c.billing.detail
	if transition {
		c.billing.blocked = false
		c.billing.since = time.Time{}
		c.billing.detail = ""
		c.billing.retryAt = time.Time{}
	}
	if transition && c.billing.hook != nil {
		c.billing.hook(false, detail)
	}
	return transition
}

// anthropicBillingBody reports whether an error response encodes the
// account billing state rather than a request problem.
func anthropicBillingBody(status int, body string) bool {
	return status == 400 && strings.Contains(strings.ToLower(body), billingBlockedMarker)
}
