package agent

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
)

// withRequestSubjects extends ctx with subject keys derived from req's
// channel binding so that subject-aware context providers (the
// archive prewarm and the subject-keyed knowledge provider) can find
// stored facts relevant to the current wake without needing the
// user's message itself to mention them. Existing subjects already on
// ctx (e.g. from an outer wake bridge that knows about
// entity:/area:/space: subjects) are preserved.
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
func withRequestSubjects(ctx context.Context, req *Request) context.Context {
	existing := knowledge.SubjectsFromContext(ctx)
	subjects := append([]string{}, existing...)

	if cb := req.ChannelBinding; cb != nil {
		if cb.ContactID != "" {
			subjects = appendUniqueSubject(subjects, "contact:"+cb.ContactID)
		}
		if cb.Address != "" {
			subjects = appendUniqueSubject(subjects, "contact:"+cb.Address)
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
