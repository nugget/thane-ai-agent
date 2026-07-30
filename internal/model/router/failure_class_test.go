package router

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
)

func TestClassifyResourceFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"canceled", context.Canceled, ""},
		{"wrapped canceled", fmt.Errorf("chat: %w", context.Canceled), ""},
		{"deadline exceeded", context.DeadlineExceeded, CooldownReasonTimeout},
		{
			"dns no such host",
			fmt.Errorf("request failed: %w", &net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: &net.DNSError{Err: "no such host", Name: "spark-a23e.example.net", IsNotFound: true},
			}),
			CooldownReasonConnection,
		},
		{
			"connection refused",
			fmt.Errorf("request failed: %w", &net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
			}),
			CooldownReasonConnection,
		},
		{
			"host unreachable",
			fmt.Errorf("request failed: %w", &net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: &os.SyscallError{Syscall: "connect", Err: syscall.EHOSTUNREACH},
			}),
			CooldownReasonConnection,
		},
		{
			"connection reset mid-request",
			fmt.Errorf("request failed: %w", &net.OpError{
				Op:  "read",
				Net: "tcp",
				Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET},
			}),
			CooldownReasonConnection,
		},
		// Errors that crossed a non-wrapping boundary arrive as strings.
		{"stringified no such host", errors.New(`API error: dial tcp: lookup spark: no such host`), CooldownReasonConnection},
		{"stringified refused", errors.New("upstream: connection refused"), CooldownReasonConnection},
		{"client timeout string", errors.New("request failed: Client.Timeout exceeded while awaiting headers"), CooldownReasonTimeout},
		{"anthropic overloaded", errors.New("API error 529: overloaded_error"), CooldownReasonTimeout},
		// Application-level failures say nothing about resource health.
		{"http 400", errors.New("API error 400: invalid request"), ""},
		{"plain error", errors.New("model produced no output"), ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ClassifyResourceFailure(tc.err); got != tc.want {
				t.Fatalf("ClassifyResourceFailure(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
