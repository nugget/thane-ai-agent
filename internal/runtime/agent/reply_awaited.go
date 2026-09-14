package agent

import "github.com/nugget/thane-ai-agent/internal/state/memory"

// replyAwaited reports whether someone is waiting on the final text of a
// turn with this message origin, which is what [iterate.Config.ReplyAwaited]
// needs to choose the repeat guard's wording.
//
// The origin stamp already encodes the distinction and every surface
// sets it: channel bridges declare memory.OriginChannel on a turn that
// carries counterparty messages (and downgrade to memory.OriginWake when
// every message was skipped and only a notify summary remains), and the
// REST, OpenAI-compatible, and Ollama-compatible surfaces declare
// memory.OriginAPI. Loop wakes default to memory.OriginWake, and
// delegates and other internal runs leave the stamp empty — none of
// those has anyone reading the final text as a reply, so they get the
// neutral wording.
func replyAwaited(origin string) bool {
	switch origin {
	case memory.OriginChannel, memory.OriginAPI:
		return true
	default:
		return false
	}
}
