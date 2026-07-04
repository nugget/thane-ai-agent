package awareness

import (
	"encoding/json"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// maxTransitionsPerSubscription aliases the declaration-level cap
// (#1210): the per-entity retention ring bounds storage; this bounds
// prompt spend. Tool boundaries reject a larger ask with a teaching
// error, and the render clamps defensively for declarations that
// arrive by other routes.
const maxTransitionsPerSubscription = looppkg.MaxSubscriptionTransitions

// TransitionSource supplies an entity's recently observed state
// changes, newest first and already translated into the class-aware
// vocabulary, plus the count that matched before the limit was
// applied. The state watcher's window provider is the runtime
// implementation; providers hold it as an interface so tests can
// substitute a fixture.
type TransitionSource interface {
	RecentTransitions(entityID string, limit int, window time.Duration) ([]homeassistant.Transition, int)
}

// mergeTransitionsIntoEntityContext appends a subscription's declared
// transition log to its rendered entity block, mirroring the history
// merge: a "transitions" key on the entity's compact JSON, with
// "transitions_truncated" advertised whenever more changes matched
// than rendered, and "transitions_unavailable" when the declaration
// asked for a log but no retention source is wired. An empty log
// renders as an empty array — "no retained changes" is a real answer,
// distinct from unavailable.
func mergeTransitionsIntoEntityContext(base string, sub looppkg.EntitySubscription, source TransitionSource, now time.Time) string {
	extra := make(map[string]any, 2)
	if source == nil {
		extra["transitions_unavailable"] = true
	} else {
		limit := sub.Transitions
		if limit <= 0 || limit > maxTransitionsPerSubscription {
			limit = maxTransitionsPerSubscription
		}
		window := time.Duration(sub.TransitionsWindowSeconds) * time.Second
		transitions, matched := source.RecentTransitions(sub.EntityID, limit, window)
		entries := make([]map[string]any, 0, len(transitions))
		for _, tr := range transitions {
			entries = append(entries, map[string]any{
				"from": tr.From,
				"to":   tr.To,
				"ago":  promptfmt.FormatDeltaOnly(tr.At, now),
			})
		}
		extra["transitions"] = entries
		if matched > len(transitions) {
			extra["transitions_truncated"] = true
		}
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(base), &payload); err == nil {
		for k, v := range extra {
			payload[k] = v
		}
		return promptfmt.MarshalCompact(payload)
	}
	return base + "\n" + promptfmt.MarshalCompact(extra)
}
