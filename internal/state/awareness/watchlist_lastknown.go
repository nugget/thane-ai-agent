package awareness

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// lastKnownGoodLookback bounds how far back we look for the most
// recent real state before an entity went into a sentinel state. A
// day is enough to stay useful for safety reasoning ("the door was
// closed at 3am") without dragging the recorder for ancient history.
const lastKnownGoodLookback = 24 * time.Hour

// enrichWithLastKnownGood adds last_state and last_state_seen fields
// to the JSON for an unavailable entity by walking recent history
// backward to find the most recent non-sentinel reading. Often the
// most safety-relevant fact: knowing the door was closed five minutes
// before the sensor died is dramatically more useful than just
// knowing the sensor is offline now.
//
// Returns the input unchanged when the entity is not in a sentinel
// state, when the history call fails, or when no real state exists
// in the lookback window — every failure mode degrades to the
// already-correct unavailable payload.
func enrichWithLastKnownGood(
	ctx context.Context,
	history StateGetter,
	base string,
	current *homeassistant.State,
	now time.Time,
) string {
	if current == nil || !isSentinelState(current.State) {
		return base
	}

	startTime := now.Add(-lastKnownGoodLookback)
	states, err := history.GetStateHistory(ctx, current.EntityID, startTime, now)
	if err != nil || len(states) == 0 {
		return base
	}

	lastState, lastSeen, ok := mostRecentRealState(states)
	if !ok {
		return base
	}

	translated := semanticState(
		entityDomain(current.EntityID),
		attrString(current.Attributes, "device_class"),
		lastState,
	)

	var payload map[string]any
	if err := json.Unmarshal([]byte(base), &payload); err != nil {
		return base
	}
	payload["last_state"] = translated
	if !lastSeen.IsZero() {
		payload["last_state_seen"] = promptfmt.FormatDeltaOnly(lastSeen, now)
	}
	return promptfmt.MarshalCompact(payload)
}

// mostRecentRealState returns the latest non-sentinel state in the
// chronologically-ordered series. Walks backward from the end so the
// first non-sentinel hit wins. Returns ok=false if every state in
// the window is sentinel.
func mostRecentRealState(states []homeassistant.State) (string, time.Time, bool) {
	for i := len(states) - 1; i >= 0; i-- {
		s := states[i]
		if isSentinelState(s.State) {
			continue
		}
		ts := s.LastChanged
		if ts.IsZero() {
			ts = s.LastUpdated
		}
		return s.State, ts, true
	}
	return "", time.Time{}, false
}
