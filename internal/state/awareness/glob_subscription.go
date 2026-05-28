package awareness

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// defaultMaxGlobExpansion caps how many entities a single glob
// subscription renders per turn. A glob subscription is re-expanded
// against live entities on every render, so an unbounded match set
// (e.g. "*_temperature" in a 15k-entity install) would flood the prompt
// each iteration. The cap bounds the per-turn cost; overflow is reported
// via a truncation marker so the model knows the view is partial.
const defaultMaxGlobExpansion = 25

// normalizeMaxGlobExpansion returns a sane cap, applying the default
// when v is non-positive.
func normalizeMaxGlobExpansion(v int) int {
	if v <= 0 {
		return defaultMaxGlobExpansion
	}
	return v
}

// lazyStates fetches the full live state snapshot once, on first use,
// and reuses it across a single render. Glob expansion needs the entity
// universe; concrete subscriptions don't, so a render with no glob
// subscriptions never calls GetStates at all.
type lazyStates struct {
	ha     StateGetter
	logger *slog.Logger
	states []homeassistant.State
	err    error
	done   bool
}

func newLazyStates(ha StateGetter, logger *slog.Logger) *lazyStates {
	return &lazyStates{ha: ha, logger: logger}
}

// get returns the live state snapshot and the fetch error, caching both
// so repeated glob expansions in one render share a single GetStates.
// The error is surfaced (not swallowed) so the caller can render an
// explicit fetch-error marker rather than hide an active glob as if it
// matched nothing.
func (l *lazyStates) get(ctx context.Context) ([]homeassistant.State, error) {
	if l.done {
		return l.states, l.err
	}
	l.done = true
	l.states, l.err = l.ha.GetStates(ctx)
	if l.err != nil && l.logger != nil {
		l.logger.Warn("failed to fetch states for glob subscription expansion", "error", l.err)
	}
	return l.states, l.err
}

// renderWatchedState renders one entity's watched-context block from an
// already-fetched state, applying the subscription's forecast, metadata,
// and history options. It is the single per-entity render shared by the
// concrete-id path and the glob-expansion path, so the two can't drift —
// the glob path passes states it pulled in bulk, avoiding a per-entity
// GetState.
func renderWatchedState(
	ctx context.Context,
	ha StateGetter,
	logger *slog.Logger,
	sub WatchedSubscription,
	state *homeassistant.State,
	now time.Time,
	registries *renderRegistries,
) string {
	state = watchlistStateWithForecast(ctx, ha, logger, sub, state, "failed to fetch watched weather forecast")

	content := formatEntityContextWithMetadata(state, now, registries.entityMetadata(sub.EntityID, state, sub.Include))
	content = enrichWithLastKnownGood(ctx, ha, content, state, now)
	content = enrichUnavailable(content, state, registries)
	if len(sub.History) == 0 {
		return content
	}

	summaries, truncated, err := buildWatchlistHistorySummaries(ctx, ha, state, sub.History, now)
	if err != nil {
		if logger != nil {
			logger.Warn("failed to fetch watched entity history",
				"entity_id", sub.EntityID, "history", sub.History, "error", err)
		}
		return content
	}
	if len(summaries) == 0 && !truncated {
		return content
	}
	return mergeHistoryIntoEntityContext(content, summaries, truncated)
}

// expandGlobSubscription renders a glob subscription by matching its
// pattern (carried in sub.EntityID) against the supplied live states,
// rendering up to maxExpansion matches — sorted for stable output, with
// the subscription's own options applied to each — and appending a
// truncation marker when more matched than the cap.
//
// statesErr is the error from the bulk GetStates that produced states.
// When non-nil the snapshot couldn't be enumerated, so the glob renders
// an explicit fetch-error marker rather than silently looking like it
// matched nothing — mirroring the concrete path's fetch_error block.
//
// exclude is the set of entity_ids already rendered elsewhere (the
// always-visible watchlist, for loop-scoped globs); matches in it are
// skipped to avoid duplicate prompt blocks. Pass nil for no exclusion.
//
// Returns "" when the glob matches nothing this turn; the subscription
// stays live and is re-evaluated next render, so a silent empty turn is
// the intended "nothing matches right now" signal rather than an error.
// states is the single bulk snapshot the caller fetched once per render,
// so expansion adds no per-entity state fetch.
func expandGlobSubscription(
	ctx context.Context,
	ha StateGetter,
	logger *slog.Logger,
	sub WatchedSubscription,
	states []homeassistant.State,
	statesErr error,
	now time.Time,
	registries *renderRegistries,
	maxExpansion int,
	exclude map[string]struct{},
) string {
	pattern := sub.EntityID
	if statesErr != nil {
		return formatGlobFetchError(pattern) + "\n"
	}
	stateByID := make(map[string]*homeassistant.State, len(states))
	matchedIDs := make([]string, 0)
	for i := range states {
		s := &states[i]
		stateByID[s.EntityID] = s
		if _, skip := exclude[s.EntityID]; skip {
			continue
		}
		if ok, _ := homeassistant.MatchEntityGlob(pattern, s.EntityID); ok {
			matchedIDs = append(matchedIDs, s.EntityID)
		}
	}
	if len(matchedIDs) == 0 {
		return ""
	}
	sort.Strings(matchedIDs)

	total := len(matchedIDs)
	truncated := false
	if cap := normalizeMaxGlobExpansion(maxExpansion); total > cap {
		matchedIDs = matchedIDs[:cap]
		truncated = true
	}

	var sb strings.Builder
	for _, id := range matchedIDs {
		matchSub := sub
		matchSub.EntityID = id
		sb.WriteString(renderWatchedState(ctx, ha, logger, matchSub, stateByID[id], now, registries))
		sb.WriteByte('\n')
	}
	if truncated {
		sb.WriteString(formatGlobTruncation(pattern, total, len(matchedIDs)))
		sb.WriteByte('\n')
	}
	return sb.String()
}

// formatGlobFetchError renders the marker emitted when the live state
// snapshot needed to expand a glob couldn't be fetched this turn. It
// mirrors the concrete path's fetch_error shape so the model reads a
// uniform "active but unavailable" signal instead of inferring the glob
// matched nothing.
func formatGlobFetchError(pattern string) string {
	return promptfmt.MarshalCompact(map[string]any{
		"glob":      pattern,
		"available": false,
		"reason":    "fetch_error",
		"note":      "could not enumerate entities to expand this glob this turn",
	})
}

// formatGlobTruncation renders the marker line appended when a glob
// matched more entities than the per-turn cap, so the model can tell the
// view is partial and narrow the pattern if it needs the rest.
func formatGlobTruncation(pattern string, matched, shown int) string {
	return promptfmt.MarshalCompact(map[string]any{
		"glob":      pattern,
		"matched":   matched,
		"shown":     shown,
		"truncated": true,
		"note":      "glob matched more entities than the per-turn cap; narrow the pattern to see the rest",
	})
}
