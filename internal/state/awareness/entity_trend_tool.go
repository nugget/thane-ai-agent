package awareness

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// EntityTrendTools exposes the ha_history tool, an on-demand recorder
// trend for one entity (or one of its numeric attributes) over a
// lookback window. Implements [tools.Provider].
type EntityTrendTools struct {
	client TrendHistoryClient
	logger *slog.Logger
}

// EntityTrendToolsConfig captures the dependencies for
// [NewEntityTrendTools]. Client is required.
type EntityTrendToolsConfig struct {
	Client TrendHistoryClient
	Logger *slog.Logger
}

func NewEntityTrendTools(cfg EntityTrendToolsConfig) *EntityTrendTools {
	if cfg.Client == nil {
		panic("awareness: EntityTrendTools requires a non-nil Client")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &EntityTrendTools{client: cfg.Client, logger: logger}
}

// Name implements [tools.Provider].
func (a *EntityTrendTools) Name() string { return "awareness.ha_history" }

// Tools implements [tools.Provider].
func (a *EntityTrendTools) Tools() []*tools.Tool {
	return []*tools.Tool{
		{
			Name: "ha_history",
			Description: "Summarize how one Home Assistant entity has trended over a recorder lookback window — " +
				"the native answer to 'how has the office temperature moved over the last 24h' or 'how often did the front door open today' " +
				"without fetching raw history yourself. Returns a numeric trend (min/max/start/end/delta/rising-falling) for numeric entities " +
				"and a discrete change summary (change count + recent states) for non-numeric ones. " +
				"By default it trends the entity's state value; pass attribute to trend a numeric value carried in an attribute " +
				"(e.g. current_temperature on a climate entity). For sustained, every-turn awareness of an entity, prefer an awareness " +
				"subscription with history windows instead of polling this from a loop.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"entity_id": map[string]any{
						"type":        "string",
						"description": "The entity to summarize, e.g. sensor.office_temperature.",
					},
					"lookback_seconds": map[string]any{
						"type":        "integer",
						"description": "How far back to summarize. Defaults to 86400 (24h); clamped to a 30-day maximum. Larger windows pull more recorder rows.",
					},
					"attribute": map[string]any{
						"type":        "string",
						"description": "Optional: trend this numeric attribute instead of the state value (e.g. current_temperature, battery). Use when the interesting value lives in an attribute rather than the state.",
					},
				},
				"required": []string{"entity_id"},
			},
			Handler: a.handleHistory,
		},
	}
}

func (a *EntityTrendTools) handleHistory(ctx context.Context, args map[string]any) (string, error) {
	entityID, _ := args["entity_id"].(string)
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}

	req := TrendRequest{EntityID: entityID}
	if v, ok := args["attribute"].(string); ok {
		req.Attribute = strings.TrimSpace(v)
	}
	if v, err := optionalIntArg(args, "lookback_seconds"); err != nil {
		return "", err
	} else if v != nil {
		req.LookbackSeconds = *v
	}

	result, err := ComputeEntityTrend(ctx, a.client, req, time.Now())
	if err != nil {
		// A bad or stale entity_id is recoverable: return the shared
		// "did you mean?" envelope rather than a raw 404 the model can't
		// act on.
		if tools.IsHAEntityNotFound(err) {
			return tools.SuggestEntityNotFound(ctx, a.client, entityID), nil
		}
		a.logger.Warn("ha_history failed",
			"entity_id", entityID,
			"attribute", req.Attribute,
			"error", err,
		)
		return "", err
	}
	return result, nil
}
