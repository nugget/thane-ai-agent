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

// ErrEmptyCompletion classifies a response the provider reported as
// finished that carried no assistant content, no tool calls and no
// reasoning: the model said nothing at all. Match it with errors.Is;
// the error text stays the provider's own diagnosis.
var ErrEmptyCompletion = errors.New("empty completion")

// ErrReasoningOnly classifies a response that ended with terminal
// evidence (a finish reason or end-of-stream marker) after carrying
// reasoning but no content and no tool calls: the model was working and
// the answer never reached the content channel. A reasoning stream that
// closed without a terminal marker is a truncation, not this class.
// Match it with errors.Is; the error text stays the provider's own
// diagnosis.
var ErrReasoningOnly = errors.New("reasoning-only completion")

// classifiedError attaches a class sentinel to a provider error without
// changing its text, so callers can switch on errors.Is instead of
// matching message fragments while logs and API responses read exactly
// as before.
type classifiedError struct {
	class error
	err   error
}

// Error returns the wrapped error's text unchanged.
func (e *classifiedError) Error() string { return e.err.Error() }

// Unwrap exposes both the class and the original chain to errors.Is
// and errors.As.
func (e *classifiedError) Unwrap() []error { return []error{e.class, e.err} }

// WithErrorClass returns err marked as belonging to class (one of the
// sentinels above). The returned error's text is err's text. A nil err
// stays nil.
func WithErrorClass(class, err error) error {
	if err == nil {
		return nil
	}
	return &classifiedError{class: class, err: err}
}
