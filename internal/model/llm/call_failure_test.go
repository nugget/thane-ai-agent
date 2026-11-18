package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

// visionShaped wraps a provider error the way the vision analyzer does:
// a class marker over a %w chain, so the provider's text is two layers
// down and never at the start of the outer message.
func visionShaped(err error) error {
	return WithErrorClass(errors.New("vision model call failed"), fmt.Errorf("vision: analyze rec-1: %w", err))
}

func TestHTTPStatus(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantOK   bool
	}{
		{"typed", &APIError{Provider: "anthropic", StatusCode: 404, Body: "x"}, 404, true},
		{"typed wrapped", fmt.Errorf("outer: %w", &APIError{StatusCode: 503}), 503, true},
		{"legacy text", errors.New(`API error 404: {"error":"no such model"}`), 404, true},
		{"legacy text with surrounding space", errors.New("  API error 429: slow down"), 429, true},
		{"legacy text wrapped", fmt.Errorf("vision: analyze rec-1: %w", errors.New("API error 404: gone")), 404, true},
		{"legacy text under a class marker", visionShaped(errors.New("API error 404: gone")), 404, true},
		{"legacy text without a colon", errors.New("API error 404"), 0, false},
		{"legacy text with a non-numeric status", errors.New("API error abc: body"), 0, false},
		{"prefix only mid-message", errors.New("upstream said API error 500: boom"), 0, false},
		{"no status", errors.New("connection refused"), 0, false},
		{"nil", nil, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, ok := HTTPStatus(tt.err)
			if code != tt.wantCode || ok != tt.wantOK {
				t.Errorf("HTTPStatus = (%d, %v), want (%d, %v)", code, ok, tt.wantCode, tt.wantOK)
			}
		})
	}
}

// timeoutNetError is a net.Error that reports a timeout.
type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

func TestClassifyCallFailure(t *testing.T) {
	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	tests := []struct {
		name string
		err  error
		want CallFailure
	}{
		{"nil", nil, CallFailureUnknown},
		{"production vision 404", visionShaped(errors.New("API error 404: {\"message\":\"The model does not exist.\"}")), CallFailureModelNotFound},
		{"typed 404", &APIError{Provider: "anthropic", StatusCode: 404}, CallFailureModelNotFound},
		{"401", errors.New("API error 401: unauthorized"), CallFailureRefused},
		{"403", &APIError{StatusCode: 403}, CallFailureRefused},
		{"billing 400 is refused, not rejected", &APIError{Provider: "anthropic", StatusCode: 400, Billing: true}, CallFailureRefused},
		{"400", errors.New("API error 400: image input not supported"), CallFailureRejected},
		{"413", &APIError{StatusCode: 413}, CallFailureRejected},
		{"422", errors.New("API error 422: unprocessable"), CallFailureRejected},
		{"429", visionShaped(errors.New("API error 429: slow down")), CallFailureRateLimited},
		{"408", errors.New("API error 408: timeout"), CallFailureTimeout},
		{"deadline", visionShaped(context.DeadlineExceeded), CallFailureTimeout},
		{"net timeout", fmt.Errorf("post: %w", timeoutNetError{}), CallFailureTimeout},
		{"dial refused", fmt.Errorf("post: %w", dialErr), CallFailureUnreachable},
		{"500", errors.New("API error 500: boom"), CallFailureProvider},
		{"503 typed", &APIError{StatusCode: 503}, CallFailureProvider},
		{"unlisted status", errors.New("API error 418: teapot"), CallFailureUnknown},
		{"plain", errors.New("something odd"), CallFailureUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyCallFailure(tt.err); got != tt.want {
				t.Errorf("ClassifyCallFailure = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCallFailureWording checks every class has its own log name and
// model-facing phrase, that phrases carry no status codes or provider
// text, and which classes count as persistent.
func TestCallFailureWording(t *testing.T) {
	classes := []struct {
		f          CallFailure
		persistent bool
	}{
		{CallFailureUnknown, false},
		{CallFailureModelNotFound, true},
		{CallFailureRefused, true},
		{CallFailureRejected, true},
		{CallFailureRateLimited, false},
		{CallFailureTimeout, false},
		{CallFailureUnreachable, false},
		{CallFailureProvider, false},
	}
	names := map[string]bool{}
	phrases := map[string]bool{}
	for _, c := range classes {
		name, phrase := c.f.String(), c.f.Phrase()
		if name == "" || phrase == "" {
			t.Errorf("class %d: empty name %q or phrase %q", c.f, name, phrase)
		}
		if names[name] || phrases[phrase] {
			t.Errorf("class %d: name %q or phrase %q repeats another class", c.f, name, phrase)
		}
		names[name], phrases[phrase] = true, true
		if strings.ContainsAny(phrase, "0123456789") {
			t.Errorf("class %v phrase carries a number: %q", c.f, phrase)
		}
		if got := c.f.Persistent(); got != c.persistent {
			t.Errorf("class %v Persistent = %v, want %v", c.f, got, c.persistent)
		}
	}
}
