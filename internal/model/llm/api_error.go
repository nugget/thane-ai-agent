package llm

import (
	"errors"
	"fmt"
)

// APIError is a provider HTTP error with its status preserved as
// structure. Providers historically flattened these into fmt.Errorf,
// which forced every downstream classifier into string matching — and
// the classifiers drifted: the agent loop's user-fixable check matched
// the bare "API error " prefix while the Anthropic provider emitted
// "anthropic API error ", so Anthropic 4xxs (including billing-state
// 400s) wrongly entered failover on every iteration. The Error text
// stays byte-identical to what each provider always produced; the
// fields are the new capability.
type APIError struct {
	// Provider is the provider family ("anthropic", ...). Empty for
	// providers whose historical text carried no prefix.
	Provider   string
	StatusCode int
	Body       string

	// Billing marks a refusal caused by the account's billing state —
	// set by the provider that recognizes its own body shapes (e.g.
	// Anthropic's credit-balance 400). Persistent and operator-
	// actionable: no retry fixes it, and every loop hitting the same
	// key learns nothing new after the first discovery.
	Billing bool
}

// Error reproduces the provider's historical string exactly.
func (e *APIError) Error() string {
	if e.Provider != "" {
		return fmt.Sprintf("%s API error %d: %s", e.Provider, e.StatusCode, e.Body)
	}
	return fmt.Sprintf("API error %d: %s", e.StatusCode, e.Body)
}

// IsBillingBlocked reports whether err (anywhere in its chain) is a
// provider refusal caused by account billing state.
func IsBillingBlocked(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Billing
}
