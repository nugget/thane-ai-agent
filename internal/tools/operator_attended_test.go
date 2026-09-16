package tools

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// TestOperatorAttended pins the one predicate email delivery and
// contact identity custody share: only the operator's own native-API
// message and their own inbound channel message are attended.
func TestOperatorAttended(t *testing.T) {
	bg := context.Background()
	api := func(channel string) context.Context {
		ctx := WithMessageOrigin(bg, memory.OriginAPI)
		if channel != "" {
			ctx = WithHints(ctx, map[string]string{"channel": channel})
		}
		return ctx
	}
	owner := &memory.ChannelBinding{Channel: "signal", Address: "+1", IsOwner: true}
	other := &memory.ChannelBinding{Channel: "signal", Address: "+2", ContactID: "x", TrustZone: "household"}
	tests := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"bare context", bg, false},
		{"native API", api("api"), true},
		{"Ollama-compatible shim", api("ollama"), false},
		{"API origin without a channel hint", api(""), false},
		{"wake", WithMessageOrigin(bg, memory.OriginWake), false},
		{"operator's own channel message", WithMessageOrigin(WithChannelBinding(bg, owner), memory.OriginChannel), true},
		{"owner-bound loop wake", WithMessageOrigin(WithChannelBinding(bg, owner), memory.OriginWake), false},
		{"owner binding with no origin", WithChannelBinding(bg, owner), false},
		{"someone else's channel message", WithMessageOrigin(WithChannelBinding(bg, other), memory.OriginChannel), false},
		{"channel origin with no binding", WithMessageOrigin(bg, memory.OriginChannel), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OperatorAttended(tt.ctx); got != tt.want {
				t.Errorf("OperatorAttended = %v, want %v", got, tt.want)
			}
		})
	}
}
