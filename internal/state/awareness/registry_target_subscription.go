package awareness

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// expandRegistryTargetSubscription renders an area/label/floor
// subscription by resolving its current members from the registry and
// rendering each with the subscription's own options — the registry
// twin of [expandGlobSubscription]. Membership is re-resolved every
// render, so the subscription tracks the home's organization rather
// than a frozen entity list.
//
// It needs both the registry (for membership) and the live-state
// snapshot (to render each member); a fetch failure of either renders
// an explicit error marker rather than looking like an empty match.
// exclude drops ids already rendered elsewhere. Returns "" when the
// target currently has no members, the intended "nothing here right
// now" signal.
func expandRegistryTargetSubscription(
	ctx context.Context,
	ha StateGetter,
	logger *slog.Logger,
	sub WatchedSubscription,
	target SubscriptionTarget,
	states []homeassistant.State,
	statesErr error,
	now time.Time,
	registries *renderRegistries,
	maxExpansion int,
	exclude map[string]struct{},
) string {
	label := target.String()
	if registries == nil {
		// No registry client wired — membership can't be resolved.
		return formatTargetFetchError(label, "registry access is unavailable, so this target's members can't be resolved this turn") + "\n"
	}
	if statesErr != nil {
		return formatTargetFetchError(label, "could not enumerate entity states to expand this target this turn") + "\n"
	}
	resolver, err := newMembershipResolver(registries)
	if err != nil {
		if logger != nil {
			logger.Warn("failed to resolve subscription target membership",
				"target", label, "error", err)
		}
		return formatTargetFetchError(label, "could not read the registry to expand this target this turn") + "\n"
	}

	stateByID := make(map[string]*homeassistant.State, len(states))
	for i := range states {
		stateByID[states[i].EntityID] = &states[i]
	}

	matchedIDs := make([]string, 0)
	for _, id := range resolver.members(target) {
		if _, skip := exclude[id]; skip {
			continue
		}
		// A registry member with no live state (disabled/never-loaded)
		// is dropped from the render — renderWatchedState needs a state,
		// and the honest empty-state case belongs to explicit reads.
		if _, ok := stateByID[id]; !ok {
			continue
		}
		matchedIDs = append(matchedIDs, id)
	}
	if len(matchedIDs) == 0 {
		return ""
	}

	return renderExpandedMatches(ctx, ha, logger, sub, label, matchedIDs, stateByID, now, registries, maxExpansion)
}

// renderExpandedMatches is the shared tail of glob and registry-target
// expansion: render each matched id with the subscription's options,
// honoring the per-turn cap and appending a truncation marker when more
// matched than shown. matchedIDs must already be sorted and filtered.
func renderExpandedMatches(
	ctx context.Context,
	ha StateGetter,
	logger *slog.Logger,
	sub WatchedSubscription,
	label string,
	matchedIDs []string,
	stateByID map[string]*homeassistant.State,
	now time.Time,
	registries *renderRegistries,
	maxExpansion int,
) string {
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
		sb.WriteString(formatTargetTruncation(label, total, len(matchedIDs)))
		sb.WriteByte('\n')
	}
	return sb.String()
}

// formatTargetFetchError mirrors the glob fetch-error marker for
// registry targets, so a target that couldn't expand reads as "active
// but unavailable" rather than inferring an empty membership.
func formatTargetFetchError(target, reason string) string {
	return promptfmt.MarshalCompact(map[string]any{
		"target":    target,
		"available": false,
		"reason":    "fetch_error",
		"note":      reason,
	})
}

// formatTargetTruncation mirrors the glob truncation marker for registry
// targets.
func formatTargetTruncation(target string, matched, shown int) string {
	return promptfmt.MarshalCompact(map[string]any{
		"target":    target,
		"matched":   matched,
		"shown":     shown,
		"truncated": true,
		"note":      fmt.Sprintf("%s has more members than the per-turn cap; scope to a smaller area/label/floor to see the rest", target),
	})
}

// String renders a target back to its stored form for display in
// markers (area:office, label:critical, binary_sensor.*door*, ...).
func (t SubscriptionTarget) String() string {
	switch t.Kind {
	case TargetArea:
		return "area:" + t.Value
	case TargetLabel:
		return "label:" + t.Value
	case TargetFloor:
		return "floor:" + t.Value
	default:
		return t.Value
	}
}
