package agent

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// withChannelSubjects extends ctx with subject keys derived from a
// resolved channel binding so subject-aware context providers (the
// archive prewarm and the subject-keyed knowledge provider) can find
// stored facts relevant to the current wake. Existing subjects on
// ctx (e.g. from an outer wake bridge that knows about
// entity:/area:/space: subjects) are preserved and deduplicated.
//
// The caller is responsible for resolving the effective binding
// before invoking — passing req.ChannelBinding directly would miss
// the persisted-binding fallback that Loop.Run uses for API turns
// supplying only conversation_id.
//
// Currently injects:
//
//   - "contact:<ContactID>" when the channel binding resolved to a
//     known contact UUID. This is the stable subject; prefer it over
//     "contact:<Address>" which can shift when phone numbers move
//     between accounts.
//   - "contact:<Address>" when the channel binding carries a raw
//     address (E.164 phone for Signal, SMTP for email). Useful even
//     without a resolved contact — the address itself is a stable
//     handle for retrieval.
//
// Loops and channels that want richer subjects (entity:, area:,
// space:) can call [knowledge.WithSubjects] before invoking the agent
// loop; this helper preserves what they set.
func withChannelSubjects(ctx context.Context, binding *memory.ChannelBinding) context.Context {
	existing := knowledge.SubjectsFromContext(ctx)
	subjects := append([]string{}, existing...)

	if binding != nil {
		if binding.ContactID != "" {
			subjects = appendUniqueSubject(subjects, "contact:"+binding.ContactID)
		}
		if binding.Address != "" {
			subjects = appendUniqueSubject(subjects, "contact:"+binding.Address)
		}
	}

	if len(subjects) == len(existing) {
		return ctx
	}
	return knowledge.WithSubjects(ctx, subjects)
}

func appendUniqueSubject(subjects []string, s string) []string {
	for _, existing := range subjects {
		if existing == s {
			return subjects
		}
	}
	return append(subjects, s)
}
