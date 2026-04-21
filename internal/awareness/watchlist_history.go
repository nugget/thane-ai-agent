package awareness

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
)

const (
	maxWatchlistHistoryWindows     = 4
	maxWatchlistRecentDiscreteKeys = 4
)

func buildWatchlistHistorySummaries(
	ctx context.Context,
	history StateGetter,
	current *homeassistant.State,
	offsets []int,
	now time.Time,
) ([]map[string]any, bool, error) {
	if current == nil || len(offsets) == 0 {
		return nil, false, nil
	}

	windows, truncated := normalizeHistoryOffsets(offsets)
	if len(windows) == 0 {
		return nil, truncated, nil
	}

	startTime := now.Add(-time.Duration(windows[len(windows)-1]) * time.Second)
	states, err := history.GetStateHistory(ctx, current.EntityID, startTime, now)
	if err != nil {
		return nil, truncated, err
	}

	series := normalizeHistoryStates(states, current)
	if len(series) == 0 {
		return nil, truncated, nil
	}

	summaries := make([]map[string]any, 0, len(windows))
	for _, offset := range windows {
		window := historyWindowStates(series, now.Add(-time.Duration(offset)*time.Second))
		if len(window) == 0 {
			continue
		}
		summary := summarizeHistoryWindow(window, current, offset)
		if len(summary) > 0 {
			summaries = append(summaries, summary)
		}
	}

	return summaries, truncated, nil
}

func mergeHistoryIntoEntityContext(base string, summaries []map[string]any, truncated bool) string {
	if len(summaries) == 0 && !truncated {
		return base
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(base), &payload); err == nil {
		payload["history"] = summaries
		if truncated {
			payload["history_truncated"] = true
		}
		return marshalCompact(payload)
	}

	fallback := map[string]any{"history": summaries}
	if truncated {
		fallback["history_truncated"] = true
	}
	return base + "\n" + marshalCompact(fallback)
}

func normalizeHistoryOffsets(offsets []int) ([]int, bool) {
	seen := make(map[int]bool, len(offsets))
	windows := make([]int, 0, len(offsets))
	for _, offset := range offsets {
		if offset <= 0 || seen[offset] {
			continue
		}
		seen[offset] = true
		windows = append(windows, offset)
	}
	sort.Ints(windows)
	if len(windows) <= maxWatchlistHistoryWindows {
		return windows, false
	}
	return windows[:maxWatchlistHistoryWindows], true
}

func normalizeHistoryStates(states []homeassistant.State, current *homeassistant.State) []homeassistant.State {
	series := append([]homeassistant.State(nil), states...)
	sort.Slice(series, func(i, j int) bool {
		return historyStateTime(series[i]).Before(historyStateTime(series[j]))
	})

	out := make([]homeassistant.State, 0, len(series)+1)
	for _, state := range series {
		if current != nil && state.EntityID == "" {
			state.EntityID = current.EntityID
		}
		if len(out) > 0 && sameHistoryState(out[len(out)-1], state) {
			continue
		}
		out = append(out, state)
	}

	if current != nil {
		currentCopy := *current
		if len(out) == 0 || !sameHistoryState(out[len(out)-1], currentCopy) {
			out = append(out, currentCopy)
		} else {
			out[len(out)-1] = currentCopy
		}
	}

	return out
}

func historyWindowStates(series []homeassistant.State, cutoff time.Time) []homeassistant.State {
	baselineIdx := -1
	for i, state := range series {
		if !historyStateTime(state).After(cutoff) {
			baselineIdx = i
			continue
		}
		break
	}

	window := make([]homeassistant.State, 0, len(series))
	if baselineIdx >= 0 {
		window = append(window, series[baselineIdx])
	}
	for _, state := range series {
		if historyStateTime(state).After(cutoff) {
			if len(window) > 0 && sameHistoryState(window[len(window)-1], state) {
				continue
			}
			window = append(window, state)
		}
	}
	if len(window) == 0 && len(series) > 0 {
		window = append(window, series[len(series)-1])
	}
	return window
}

func summarizeHistoryWindow(window []homeassistant.State, current *homeassistant.State, offsetSeconds int) map[string]any {
	if len(window) == 0 {
		return nil
	}

	if summary, ok := summarizeNumericHistory(window, current, offsetSeconds); ok {
		return summary
	}
	return summarizeDiscreteHistory(window, offsetSeconds)
}

func summarizeNumericHistory(window []homeassistant.State, current *homeassistant.State, offsetSeconds int) (map[string]any, bool) {
	values := make([]float64, 0, len(window))
	for _, state := range window {
		value, err := strconv.ParseFloat(strings.TrimSpace(state.State), 64)
		if err != nil {
			return nil, false
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		return nil, false
	}

	places := historyPrecision(current)
	first := values[0]
	last := values[len(values)-1]
	minValue := values[0]
	maxValue := values[0]
	for _, value := range values[1:] {
		if value < minValue {
			minValue = value
		}
		if value > maxValue {
			maxValue = value
		}
	}

	delta := last - first
	summary := map[string]any{
		"lookback":     historyLookbackDelta(offsetSeconds),
		"kind":         "numeric",
		"sample_count": len(values),
		"start_state":  roundFloat(first, places),
		"end_state":    roundFloat(last, places),
		"value_delta":  formatSignedFloat(delta, places),
		"min_value":    roundFloat(minValue, places),
		"max_value":    roundFloat(maxValue, places),
		"trend":        numericTrendLabel(delta),
	}
	return summary, true
}

func summarizeDiscreteHistory(window []homeassistant.State, offsetSeconds int) map[string]any {
	if len(window) == 0 {
		return nil
	}

	changes := 0
	recent := make([]string, 0, maxWatchlistRecentDiscreteKeys)
	recentTruncated := false
	for i, state := range window {
		if i > 0 && window[i-1].State != state.State {
			changes++
		}
		if len(recent) == 0 || recent[len(recent)-1] != state.State {
			recent = append(recent, state.State)
			if len(recent) > maxWatchlistRecentDiscreteKeys {
				recent = recent[len(recent)-maxWatchlistRecentDiscreteKeys:]
				recentTruncated = true
			}
		}
	}

	summary := map[string]any{
		"lookback":                historyLookbackDelta(offsetSeconds),
		"kind":                    "discrete",
		"sample_count":            len(window),
		"change_count":            changes,
		"start_state":             window[0].State,
		"end_state":               window[len(window)-1].State,
		"recent_states":           recent,
		"recent_states_truncated": recentTruncated,
	}
	if changes == 0 {
		summary["trend"] = "stable"
	}
	return summary
}

func numericTrendLabel(delta float64) string {
	switch {
	case delta > 0.001:
		return "rising"
	case delta < -0.001:
		return "falling"
	default:
		return "flat"
	}
}

func formatSignedFloat(value float64, places int) string {
	formatted := roundFloat(value, places)
	if value > 0 {
		return "+" + formatted
	}
	return formatted
}

func historyLookbackDelta(offsetSeconds int) string {
	base := time.Unix(0, 0).UTC()
	return FormatDeltaOnly(base.Add(-time.Duration(offsetSeconds)*time.Second), base)
}

func historyPrecision(current *homeassistant.State) int {
	if current == nil {
		return 2
	}
	return statePrecision(attrString(current.Attributes, "device_class"))
}

func historyStateTime(state homeassistant.State) time.Time {
	if !state.LastChanged.IsZero() {
		return state.LastChanged
	}
	return state.LastUpdated
}

func sameHistoryState(a, b homeassistant.State) bool {
	return a.State == b.State && historyStateTime(a).Equal(historyStateTime(b))
}
