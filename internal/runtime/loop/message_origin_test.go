package loop

import (
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// TestPrepareAgentTurnRequestMessageOrigin covers the provenance default
// at the loop's request-preparation boundary: an undeclared turn-builder
// turn is by definition an internally-originated wake, while a builder
// that declares its origin (a channel bridge declaring OriginChannel)
// must not have that declaration overwritten. A wrong default here would
// stamp counterparty contact as internal — or wake prompts as contact —
// and poison every downstream contact-gap computation.
func TestPrepareAgentTurnRequestMessageOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		declared string
		want     string
	}{
		{"undeclared defaults to wake", "", memory.OriginWake},
		{"channel declaration preserved", memory.OriginChannel, memory.OriginChannel},
		{"api declaration preserved", memory.OriginAPI, memory.OriginAPI},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := New(Config{Name: "origin-test", Task: "t"}, Deps{Runner: &noopRunner{}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			req, err := l.prepareAgentTurnRequest(Request{MessageOrigin: tt.declared}, "conv-1", false)
			if err != nil {
				t.Fatalf("prepareAgentTurnRequest: %v", err)
			}
			if req.MessageOrigin != tt.want {
				t.Errorf("MessageOrigin = %q, want %q", req.MessageOrigin, tt.want)
			}
		})
	}
}
