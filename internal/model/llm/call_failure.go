package llm

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
)

// HTTPStatus reports the HTTP status a provider returned for a failed
// call, found anywhere in err's chain. A typed [APIError] is read
// first. Providers that still flatten their errors into text
// ("API error 404: …", as openai_compat, ollama and lmstudio do) are
// read from the text of each error in the chain, so a wrapped failure
// ("vision: analyze …: API error 404: …") still yields its status.
func HTTPStatus(err error) (int, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode, true
	}
	return legacyHTTPStatus(err)
}

func legacyHTTPStatus(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	if code, ok := LegacyAPIErrorStatus(err.Error()); ok {
		return code, true
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return legacyHTTPStatus(u.Unwrap())
	case interface{ Unwrap() []error }:
		for _, inner := range u.Unwrap() {
			if code, ok := legacyHTTPStatus(inner); ok {
				return code, true
			}
		}
	}
	return 0, false
}

// LegacyAPIErrorStatus parses the status from a provider error message
// of the untyped form "API error NNN: body". It matches only a message
// that begins with that prefix, after trimming space.
func LegacyAPIErrorStatus(msg string) (int, bool) {
	msg = strings.TrimSpace(msg)
	if !strings.HasPrefix(msg, "API error ") {
		return 0, false
	}
	codeField, _, ok := strings.Cut(strings.TrimPrefix(msg, "API error "), ":")
	if !ok {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(codeField))
	if err != nil {
		return 0, false
	}
	return code, true
}

// CallFailure classifies why a model call failed, in terms that decide
// what a model reading about the failure can do next.
type CallFailure int

const (
	// CallFailureUnknown is a failure none of the other classes match.
	CallFailureUnknown CallFailure = iota
	// CallFailureModelNotFound is a 404: the provider has no model by
	// the requested name.
	CallFailureModelNotFound
	// CallFailureRefused is a 401 or 403, or a refusal caused by the
	// account's billing state.
	CallFailureRefused
	// CallFailureRejected is a 400, 413 or 422: the provider found the
	// request itself unacceptable, as when an image goes to a model
	// that does not take images.
	CallFailureRejected
	// CallFailureRateLimited is a 429.
	CallFailureRateLimited
	// CallFailureTimeout is a deadline that expired before the provider
	// answered, or a 408.
	CallFailureTimeout
	// CallFailureUnreachable is a network failure before any HTTP
	// status: the provider could not be reached.
	CallFailureUnreachable
	// CallFailureProvider is a 5xx: the provider failed while handling
	// the request.
	CallFailureProvider
)

// ClassifyCallFailure classifies a failed model call from its error.
func ClassifyCallFailure(err error) CallFailure {
	if err == nil {
		return CallFailureUnknown
	}
	if IsBillingBlocked(err) {
		return CallFailureRefused
	}
	if code, ok := HTTPStatus(err); ok {
		switch {
		case code == 404:
			return CallFailureModelNotFound
		case code == 401 || code == 403:
			return CallFailureRefused
		case code == 400 || code == 413 || code == 422:
			return CallFailureRejected
		case code == 429:
			return CallFailureRateLimited
		case code == 408:
			return CallFailureTimeout
		case code >= 500 && code < 600:
			return CallFailureProvider
		}
		return CallFailureUnknown
	}
	// context.DeadlineExceeded also satisfies net.Error with Timeout
	// true. It is matched by name first so the class does not rest on
	// that.
	if errors.Is(err, context.DeadlineExceeded) {
		return CallFailureTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return CallFailureTimeout
		}
		return CallFailureUnreachable
	}
	return CallFailureUnknown
}

// String names the class for logs.
func (f CallFailure) String() string {
	switch f {
	case CallFailureModelNotFound:
		return "model_not_found"
	case CallFailureRefused:
		return "refused"
	case CallFailureRejected:
		return "rejected"
	case CallFailureRateLimited:
		return "rate_limited"
	case CallFailureTimeout:
		return "timeout"
	case CallFailureUnreachable:
		return "unreachable"
	case CallFailureProvider:
		return "provider_error"
	}
	return "unknown"
}

// Phrase states the failure for a model to read. It is fixed text: it
// never carries a model name, a host, or the provider's response body,
// which is why model-facing failure text is built from it rather than
// from err.Error().
func (f CallFailure) Phrase() string {
	switch f {
	case CallFailureModelNotFound:
		return "the provider reported that the requested model does not exist"
	case CallFailureRefused:
		return "the provider refused the request"
	case CallFailureRejected:
		return "the provider rejected the request as one that model cannot handle"
	case CallFailureRateLimited:
		return "the provider was rejecting requests as over its rate limit"
	case CallFailureTimeout:
		return "the provider did not answer in time"
	case CallFailureUnreachable:
		return "the provider could not be reached"
	case CallFailureProvider:
		return "the provider failed while handling the request"
	}
	return "the request failed"
}

// Persistent reports whether repeating the same call is expected to
// fail the same way until something is reconfigured. Transient classes
// and unknown failures report false.
func (f CallFailure) Persistent() bool {
	return f == CallFailureModelNotFound || f == CallFailureRefused || f == CallFailureRejected
}
