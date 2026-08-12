package router

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
)

// Cooldown reason strings produced by [ClassifyResourceFailure] and
// surfaced through [ResourceHealth.CooldownReason].
const (
	// CooldownReasonTimeout marks a resource that accepted a request but
	// timed out or reported overload while serving it.
	CooldownReasonTimeout = "recent timeout"

	// CooldownReasonConnection marks a resource that could not be
	// reached at all: DNS failure, connection refused, or no route.
	CooldownReasonConnection = "recent connection failure"
)

// ClassifyResourceFailure maps a request error to the cooldown reason
// it merits, or "" when the failure says nothing about resource health
// (caller cancellation, application-level errors). Callers pass the
// result directly to [Router.RecordFailure].
//
// Connection-class failures matter as much as timeouts: a runner whose
// hostname stopped resolving fails in milliseconds, and before this
// classification existed those fast failures never cooled the resource,
// so routing re-selected the dead runner indefinitely.
func ClassifyResourceFailure(err error) string {
	if err == nil {
		return ""
	}

	// Caller-driven cancellation is not a statement about the resource.
	if errors.Is(err, context.Canceled) {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CooldownReasonTimeout
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return CooldownReasonConnection
	}

	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ECONNREFUSED, syscall.ECONNRESET,
			syscall.EHOSTUNREACH, syscall.ENETUNREACH:
			return CooldownReasonConnection
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return CooldownReasonTimeout
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return CooldownReasonConnection
	}

	// String fallbacks for errors that crossed a non-wrapping boundary
	// (provider adapters, upstream HTTP bodies). "overloaded" and status
	// 529 are Anthropic's overload signals; treat them like timeouts so
	// the resource gets breathing room.
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout"),
		strings.Contains(msg, "overloaded"),
		mentionsStatus529(msg):
		return CooldownReasonTimeout
	case strings.Contains(msg, "no such host"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "no route to host"),
		strings.Contains(msg, "network is unreachable"):
		return CooldownReasonConnection
	}
	return ""
}

// mentionsStatus529 reports whether msg carries HTTP status 529 the way
// a status code actually renders: as its own whitespace-delimited token,
// at most wearing sentence punctuation ("API error 529: overloaded",
// "status 529", a leading "529 Overloaded"). A bare substring match
// misfired here — request IDs and token counts routinely embed the
// digits ("req_a1529bc", "1529 tokens"), and each false positive
// silently cooled a healthy resource for the full cooldown window.
func mentionsStatus529(msg string) bool {
	for _, field := range strings.Fields(msg) {
		if strings.Trim(field, `:;,.()[]{}"'`) == "529" {
			return true
		}
	}
	return false
}
