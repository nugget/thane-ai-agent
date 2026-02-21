package metacognitive

import (
	"context"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// RegisterTools registers metacognitive-specific tools on the given
// registry. Currently only set_next_sleep, which the LLM calls to
// control the loop's next sleep duration.
//
// The handler captures the [Loop] pointer via closure so it can
// communicate the chosen duration back to the loop goroutine. This
// follows the same pattern as session_working_memory tools.
func (l *Loop) RegisterTools(registry *tools.Registry) {
	registry.Register(&tools.Tool{
		Name: "set_next_sleep",
		Description: "Set how long the metacognitive loop should sleep before the next iteration. " +
			"Call this at the end of your analysis to control your attention cycle. " +
			"Short sleep (2–5m) for active situations needing monitoring. " +
			"Long sleep (15–30m) for quiet periods. " +
			"If you don't call this, a default sleep duration is used.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"duration": map[string]any{
					"type":        "string",
					"description": "Sleep duration as a Go duration string (e.g., '5m', '15m', '2m30s'). Clamped to configured min/max bounds.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Brief explanation of why this duration was chosen (logged for debugging).",
				},
			},
			"required": []string{"duration"},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			durStr, _ := args["duration"].(string)
			if durStr == "" {
				return "", fmt.Errorf("duration is required")
			}

			d, err := time.ParseDuration(durStr)
			if err != nil {
				return "", fmt.Errorf("invalid duration %q: %w", durStr, err)
			}

			reason, _ := args["reason"].(string)

			// Clamp to configured bounds.
			if d < l.config.MinSleep {
				d = l.config.MinSleep
			}
			if d > l.config.MaxSleep {
				d = l.config.MaxSleep
			}

			l.setNextSleep(d)
			l.deps.Logger.Info("metacognitive sleep set",
				"duration", d,
				"reason", reason,
			)

			return fmt.Sprintf("Next sleep set to %s (bounds: %s–%s).",
				d, l.config.MinSleep, l.config.MaxSleep), nil
		},
	})
}
