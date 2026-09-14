package agent

import (
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// TestReplyAwaited pins which turns keep the interactive repeat-guard
// text: only a turn that carries counterparty or API input, whose final
// text someone is waiting to read.
func TestReplyAwaited(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		{origin: memory.OriginChannel, want: true},
		{origin: memory.OriginAPI, want: true},
		{origin: memory.OriginWake, want: false},
		{origin: memory.OriginInternal, want: false},
		{origin: "", want: false},
	}
	for _, tc := range cases {
		t.Run("origin="+tc.origin, func(t *testing.T) {
			if got := replyAwaited(tc.origin); got != tc.want {
				t.Errorf("replyAwaited(%q) = %v, want %v", tc.origin, got, tc.want)
			}
		})
	}
}
