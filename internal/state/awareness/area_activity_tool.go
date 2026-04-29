package awareness

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// AreaActivityTools exposes the area_activity tool, an on-demand
// snapshot of one Home Assistant area's current state plus a
// cross-entity timeline of recent transitions. Implements
// [tools.Provider].
type AreaActivityTools struct {
	client AreaActivityClient
	logger *slog.Logger
}

// AreaActivityToolsConfig captures the dependencies for
// [NewAreaActivityTools]. Client is required.
type AreaActivityToolsConfig struct {
	Client AreaActivityClient
	Logger *slog.Logger
}

func NewAreaActivityTools(cfg AreaActivityToolsConfig) *AreaActivityTools {
	if cfg.Client == nil {
		panic("awareness: AreaActivityTools requires a non-nil Client")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &AreaActivityTools{client: cfg.Client, logger: logger}
}

// Name implements [tools.Provider].
func (a *AreaActivityTools) Name() string { return "awareness.area_activity" }

// Tools implements [tools.Provider].
func (a *AreaActivityTools) Tools() []*tools.Tool {
	return []*tools.Tool{
		{
			Name: "get_area_activity",
			Description: "Get a snapshot of one Home Assistant area's current state plus a timeline of recent transitions. " +
				"Returns entities bucketed by relevance: anomalies (offline or in alarm state) first, then active devices " +
				"(lights on, climate running, media playing), then recent changes within the lookback window, then ambient " +
				"baseline sensors (temperature, humidity), then the rest (capped). Plus a cross-entity timeline of " +
				"discrete state transitions ordered newest-first. Use this to answer 'what's happening in <room>' or " +
				"'is everything okay in <room>' without polling individual entities.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"area": map[string]any{
						"type":        "string",
						"description": "The area to inspect. Accepts the area_id (e.g. \"kitchen\"), the human-friendly name (e.g. \"Kitchen\"), or a configured alias.",
					},
					"lookback_seconds": map[string]any{
						"type":        "integer",
						"description": "Window for recent_changes bucketing and the timeline event stream. Defaults to 3600 (1 hour). Larger windows pull more logbook events.",
					},
					"include_diagnostic": map[string]any{
						"type":        "boolean",
						"description": "When true, includes entities marked as diagnostic or config (battery levels, signal strength, firmware version). Default false — those entities clutter the snapshot when answering 'what's happening'.",
					},
					"max_stable": map[string]any{
						"type":        "integer",
						"description": "Cap on the stable bucket (entities not in any other bucket). Default 5. Truncated count is reported via stable_truncated_count.",
					},
				},
				"required": []string{"area"},
			},
			Handler: a.handleAreaActivity,
		},
	}
}

func (a *AreaActivityTools) handleAreaActivity(ctx context.Context, args map[string]any) (string, error) {
	area, _ := args["area"].(string)
	if area == "" {
		return "", fmt.Errorf("area is required")
	}

	req := AreaActivityRequest{Area: area}
	if v, err := optionalIntArg(args, "lookback_seconds"); err != nil {
		return "", err
	} else if v != nil {
		req.LookbackSeconds = *v
	}
	if v, ok := args["include_diagnostic"].(bool); ok {
		req.IncludeDiagnostic = v
	}
	if v, err := optionalIntArg(args, "max_stable"); err != nil {
		return "", err
	} else if v != nil {
		req.MaxStable = *v
	}

	result, err := ComputeAreaActivity(ctx, a.client, req, time.Now())
	if err != nil {
		a.logger.Warn("area_activity failed",
			"area", area,
			"error", err,
		)
		return "", err
	}
	return result, nil
}

// optionalIntArg pulls an optional integer argument out of the
// model-supplied args map. Returns nil when the key is absent,
// errors when the key is present but not an integer.
func optionalIntArg(args map[string]any, key string) (*int, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case int:
		return &v, nil
	case int64:
		x := int(v)
		return &x, nil
	case float64:
		x := int(v)
		return &x, nil
	}
	return nil, fmt.Errorf("%s must be an integer", key)
}
