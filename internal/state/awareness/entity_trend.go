package awareness

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

const (
	defaultTrendLookback = 86400   // 24h
	maxTrendLookback     = 2592000 // 30d — bounds recorder load; clamped, not rejected
)

// TrendHistoryClient is the slice of Home Assistant calls the ha_history
// tool needs: the current state (for classification, precision, and
// unit) plus recorder-backed history. The attributes-enabled history
// path is used only when a specific attribute is being trended, so a
// plain state trend stays on the lean no_attributes fetch. GetEntities
// powers the "did you mean?" suggestion when the requested entity_id
// isn't found. The concrete homeassistant.Client implements all four.
type TrendHistoryClient interface {
	GetState(ctx context.Context, entityID string) (*homeassistant.State, error)
	GetStateHistory(ctx context.Context, entityID string, startTime, endTime time.Time) ([]homeassistant.State, error)
	GetStateHistoryWithAttributes(ctx context.Context, entityID string, startTime, endTime time.Time) ([]homeassistant.State, error)
	GetEntities(ctx context.Context, domain string) ([]homeassistant.EntityInfo, error)
}

// TrendRequest describes one ha_history invocation.
type TrendRequest struct {
	EntityID        string
	LookbackSeconds int    // <= 0 falls back to default; clamped to maxTrendLookback
	Attribute       string // optional: trend this numeric attribute instead of the state value
}

// ComputeEntityTrend summarizes how one entity (or one of its numeric
// attributes) has moved over a lookback window, reusing the same
// numeric/discrete summarization the always-on watchlist injection uses.
// It returns a single compact JSON object: a numeric trend
// (min/max/start/end/delta/trend) for numeric series, a discrete change
// summary (change_count/recent_states) otherwise, or an explicit
// no-history marker when the recorder has nothing in the window.
func ComputeEntityTrend(ctx context.Context, client TrendHistoryClient, req TrendRequest, now time.Time) (string, error) {
	if client == nil {
		return "", fmt.Errorf("ha_history: client is required")
	}
	if req.EntityID == "" {
		return "", fmt.Errorf("ha_history: entity_id is required")
	}

	lookback := req.LookbackSeconds
	if lookback <= 0 {
		lookback = defaultTrendLookback
	}
	if lookback > maxTrendLookback {
		lookback = maxTrendLookback
	}

	current, err := client.GetState(ctx, req.EntityID)
	if err != nil {
		return "", fmt.Errorf("ha_history: get current state: %w", err)
	}

	windows, _ := normalizeHistoryOffsets([]int{lookback})
	startTime := now.Add(-time.Duration(lookback) * time.Second)

	var states []homeassistant.State
	summaryCurrent := current
	if req.Attribute != "" {
		// Trend a value carried in an attribute (e.g. current_temperature
		// on a climate entity): pull the attributes-enabled history, then
		// project each sample's attribute into the state slot so the shared
		// numeric/discrete summarizer can work unmodified.
		raw, histErr := client.GetStateHistoryWithAttributes(ctx, req.EntityID, startTime, now)
		if histErr != nil {
			return "", fmt.Errorf("ha_history: get history: %w", histErr)
		}
		states = projectAttributeSeries(raw, req.Attribute)
		summaryCurrent = projectAttributeState(current, req.Attribute)
	} else {
		raw, histErr := client.GetStateHistory(ctx, req.EntityID, startTime, now)
		if histErr != nil {
			return "", fmt.Errorf("ha_history: get history: %w", histErr)
		}
		states = raw
	}

	noHistory := func() string {
		payload := map[string]any{
			"entity_id": req.EntityID,
			"lookback":  historyLookbackDelta(lookback),
			"available": false,
			"reason":    "no_history",
			"note":      "no recorder history for this entity in the lookback window (it may not be recorded)",
		}
		if req.Attribute != "" {
			payload["attribute"] = req.Attribute
		}
		return promptfmt.MarshalCompact(payload)
	}

	// No recorder rows in the window → an explicit no-history marker. We
	// check the raw fetched series here rather than the summary count
	// because summarizeHistorySeries always folds in the current state,
	// so a non-recorded entity would otherwise masquerade as a flat
	// one-sample trend.
	if len(states) == 0 {
		return noHistory(), nil
	}

	summaries := summarizeHistorySeries(states, summaryCurrent, windows, now)
	if len(summaries) == 0 {
		return noHistory(), nil
	}

	// Exactly one window was requested, so flatten its summary into the
	// top-level object for a lean, model-friendly shape.
	payload := summaries[0]
	payload["entity_id"] = req.EntityID
	if req.Attribute != "" {
		payload["attribute"] = req.Attribute
	}
	if unit := attrString(current.Attributes, "unit_of_measurement"); unit != "" && payload["kind"] == "numeric" {
		payload["unit"] = unit
	}

	return promptfmt.MarshalCompact(payload), nil
}

// projectAttributeSeries returns a copy of states with each sample's
// state value replaced by the named attribute's value (string-coerced).
// Samples missing the attribute or carrying a non-coercible value are
// dropped, so the resulting series reflects only points where the
// attribute was actually recorded.
func projectAttributeSeries(states []homeassistant.State, attribute string) []homeassistant.State {
	out := make([]homeassistant.State, 0, len(states))
	for _, s := range states {
		raw, ok := s.Attributes[attribute]
		if !ok {
			continue
		}
		value, ok := attributeValueString(raw)
		if !ok {
			continue
		}
		next := s
		next.State = value
		out = append(out, next)
	}
	return out
}

// projectAttributeState returns a copy of current with its state value
// replaced by the named attribute's value, preserving the attribute map
// so precision/unit resolution still works. Returns nil when the
// attribute is absent or non-coercible on the current state: the shared
// summarizer appends the current sample to the series, so handing back
// the *unprojected* state (e.g. a climate entity's "heat") would
// contaminate a projected numeric attribute series and force a discrete
// fallback. Dropping the current sample instead lets the recorded
// attribute history summarize cleanly (or yield a no-history marker
// when the recorder also has nothing for the attribute).
func projectAttributeState(current *homeassistant.State, attribute string) *homeassistant.State {
	if current == nil {
		return nil
	}
	raw, ok := current.Attributes[attribute]
	if !ok {
		return nil
	}
	value, ok := attributeValueString(raw)
	if !ok {
		return nil
	}
	next := *current
	next.State = value
	return &next
}

// attributeValueString coerces a JSON attribute value into the string
// form the history summarizer parses. Numbers are rendered without a
// fixed precision so the numeric path can re-parse them faithfully.
func attributeValueString(raw any) (string, bool) {
	switch v := raw.(type) {
	case string:
		return v, true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case int:
		return strconv.Itoa(v), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case bool:
		return strconv.FormatBool(v), true
	default:
		return "", false
	}
}
