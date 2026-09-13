package tools

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// OperatorAttended reports whether the current turn is the operator's
// own message. Exactly two turns qualify: a message sent through
// Thane's native API (message origin API with the routing hint
// channel == "api", which the console and REST clients the operator
// drives set), and an inbound channel message (message origin channel)
// in a conversation whose channel binding is the operator's own
// contact (IsOwner).
//
// Nothing else is attended. Loop wakes, scheduled loops, and loops
// launched from the operator's conversation are not, even though they
// inherit the operator's binding, because nobody typed the turn. The
// Ollama-compatible shim, which Home Assistant automations and voice
// satellites call, is not. Other people's conversations are not.
//
// Email delivery and contact identity custody both decide on this one
// predicate, so the two can never disagree about who is present.
func OperatorAttended(ctx context.Context) bool {
	switch MessageOriginFromContext(ctx) {
	case memory.OriginAPI:
		return HintsFromContext(ctx)["channel"] == "api"
	case memory.OriginChannel:
		binding := ChannelBindingFromContext(ctx)
		return binding != nil && binding.IsOwner
	}
	return false
}
