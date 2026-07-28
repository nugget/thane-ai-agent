package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// SetNextSleepToolName is the tool a timer-driven service loop calls to
// choose its own next interval. It is registered globally so the model
// can always see it, and shadowed per-loop by
// [Loop.sleepControlRuntimeTool] so the advertisement names real bounds.
const SetNextSleepToolName = "set_next_sleep"

// sleepControlRuntimeTool builds this loop's private set_next_sleep,
// whose description states the envelope THIS loop is clamped to instead
// of the global tool's "clamped to the loop's configured bounds" — a
// sentence that names no numbers and so tells the caller nothing it can
// act on (#1313).
//
// Runtime tools shadow the global registration by name and are exempt
// from tag filtering, which is right for this one: a loop's control over
// its own cadence is part of the runtime contract it runs under, not a
// capability it might or might not have been granted.
//
// Returns ok=false for a loop with no sleep to choose. The gate is
// [newSleepEnvelope]'s, so the advertised tool and the envelope on every
// other surface appear and disappear together.
func (l *Loop) sleepControlRuntimeTool() (RuntimeTool, bool) {
	env := newSleepEnvelope(
		string(effectiveOperation(l.config.Operation)),
		l.isEventDriven(),
		l.config.SleepMin, l.config.SleepMax, l.config.SleepDefault, l.config.Jitter,
	)
	if env == nil {
		return RuntimeTool{}, false
	}
	return RuntimeTool{
		Name:        SetNextSleepToolName,
		Description: sleepControlDescription(env),
		Parameters:  sleepControlParameters(env),
		Handler:     l.HandleSetNextSleep,
	}, true
}

// sleepControlDescription writes the envelope into the advertisement the
// model reads while choosing an argument. Stating the clamp as a clamp
// matters as much as stating the bounds: a request outside the range
// succeeds at a different value, so a caller that assumed refusal would
// read "ok" and never learn its judgement had been moved.
func sleepControlDescription(env *SleepEnvelope) string {
	desc := fmt.Sprintf(
		"Choose how long this loop sleeps before its next iteration. This loop's envelope is %s to %s; a duration outside it is moved to the nearest bound and reported back as clamped, not refused. Ending an iteration without calling this leaves the sleep at the %s default.",
		env.Min, env.Max, env.Default,
	)
	if env.Jitter > 0 {
		desc += fmt.Sprintf(" The runtime then randomizes the result by up to ±%d%%, so the wake lands near the chosen duration rather than exactly on it.", int(env.Jitter*100))
	}
	return desc
}

func sleepControlParameters(env *SleepEnvelope) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"duration": map[string]any{
				"description": fmt.Sprintf(
					"Requested next sleep, as a Go duration string between %s and %s (e.g. %q or %q). A bare number is accepted as a minute count for tolerant local-model compatibility.",
					env.Min, env.Max, env.Min, env.Default,
				),
				"anyOf": []map[string]any{
					{"type": "string"},
					{"type": "number"},
					{"type": "integer"},
				},
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "Optional short explanation of why this duration was chosen. Logged for operator visibility.",
			},
		},
		"required": []string{"duration"},
	}
}

// HandleSetNextSleep applies a requested sleep to this loop and reports
// what it became. It is the single implementation behind both the global
// set_next_sleep (which resolves the running loop from the call context
// and delegates here) and the per-loop shadow above, so the two can never
// diverge on clamping, logging, or the shape of what comes back.
func (l *Loop) HandleSetNextSleep(ctx context.Context, args map[string]any) (string, error) {
	name := l.config.Name
	if op := effectiveOperation(l.config.Operation); op != OperationService {
		return "", fmt.Errorf("set_next_sleep is only available to service loops; current loop %q uses %q", name, op)
	}
	if l.isEventDriven() {
		return "", fmt.Errorf("set_next_sleep is unavailable for event-driven service loops; current loop %q waits for events instead of sleeping on a timer", name)
	}

	requested, requestedText, err := parseNextSleepDurationArg(args)
	if err != nil {
		return "", err
	}
	applied := requested
	if applied < l.config.SleepMin {
		applied = l.config.SleepMin
	}
	if applied > l.config.SleepMax {
		applied = l.config.SleepMax
	}
	reason := toolargs.TrimmedString(args, "reason")
	clamped := applied != requested
	l.SetNextSleep(applied)

	logging.Logger(ctx).Info(
		"loop next sleep set",
		"loop_id", l.id,
		"loop_name", name,
		"requested", requested.Round(time.Second),
		"applied", applied.Round(time.Second),
		"sleep_min", l.config.SleepMin,
		"sleep_max", l.config.SleepMax,
		"reason", reason,
		"clamped", clamped,
	)

	env := newSleepEnvelope(
		string(effectiveOperation(l.config.Operation)), l.isEventDriven(),
		l.config.SleepMin, l.config.SleepMax, l.config.SleepDefault, l.config.Jitter,
	)
	out, err := json.Marshal(map[string]any{
		"status":    "ok",
		"loop_id":   l.id,
		"loop_name": name,
		"requested": requestedText,
		"applied":   promptfmt.FormatDuration(applied),
		"clamped":   clamped,
		// The same object the canonical row and the self-context block
		// carry, so a loop that reads the envelope here and the envelope
		// there is reading one fact in one shape.
		"sleep_envelope": env,
		"reason":         reason,
	})
	if err != nil {
		return "", fmt.Errorf("marshal set_next_sleep result: %w", err)
	}
	return string(out), nil
}

// parseNextSleepDurationArg reads the duration argument. A bare number is
// read as minutes rather than rejected: local models routinely emit one,
// and a minute count is the only reading that is ever sensible for a
// loop's sleep.
func parseNextSleepDurationArg(args map[string]any) (time.Duration, string, error) {
	raw, ok := args["duration"]
	if !ok {
		return 0, "", fmt.Errorf("duration is required")
	}

	var durStr string
	switch v := raw.(type) {
	case string:
		durStr = strings.TrimSpace(v)
	case int:
		durStr = fmt.Sprintf("%dm", v)
	case int64:
		durStr = fmt.Sprintf("%dm", v)
	case float32:
		durStr = strconv.FormatFloat(float64(v), 'f', -1, 64) + "m"
	case float64:
		durStr = strconv.FormatFloat(v, 'f', -1, 64) + "m"
	default:
		return 0, "", fmt.Errorf("duration must be a Go duration string or a numeric minute count")
	}
	if durStr == "" {
		return 0, "", fmt.Errorf("duration is required")
	}
	d, err := time.ParseDuration(durStr)
	if err != nil {
		return 0, "", fmt.Errorf("invalid duration %q: %w", durStr, err)
	}
	return d, durStr, nil
}
