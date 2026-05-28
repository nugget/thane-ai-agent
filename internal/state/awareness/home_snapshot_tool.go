package awareness

import (
	"context"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// HomeSnapshotTools exposes the ha_home_snapshot tool, a single curated
// cross-domain "how's the house right now" view. Implements
// [tools.Provider].
type HomeSnapshotTools struct {
	client HARegistryClient
	logger *slog.Logger
}

// HomeSnapshotToolsConfig captures the dependencies for
// [NewHomeSnapshotTools]. Client is required.
type HomeSnapshotToolsConfig struct {
	Client HARegistryClient
	Logger *slog.Logger
}

func NewHomeSnapshotTools(cfg HomeSnapshotToolsConfig) *HomeSnapshotTools {
	if cfg.Client == nil {
		panic("awareness: HomeSnapshotTools requires a non-nil Client")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &HomeSnapshotTools{client: cfg.Client, logger: logger}
}

// Name implements [tools.Provider].
func (a *HomeSnapshotTools) Name() string { return "awareness.ha_home_snapshot" }

// Tools implements [tools.Provider].
func (a *HomeSnapshotTools) Tools() []*tools.Tool {
	return []*tools.Tool{
		{
			Name: "ha_home_snapshot",
			Description: "Get a single curated 'how's the house right now' overview across domains — the native answer to " +
				"'what's going on at home', 'is the house buttoned up', 'who's home'. Leads with what's actionable: anomalies " +
				"(offline or in alarm state), then security/openings (open doors/windows, unlocked locks, armed/triggered alarm " +
				"panels), then presence (who's home vs away), then climate (thermostats). A top-level summary gives the counts at " +
				"a glance, and status:quiet means nothing is offline, open, unlocked, or armed. Pass include_energy for an energy " +
				"section, and include for per-entity area/device/label/description/visibility metadata. For a single room use " +
				"get_area_activity; for one device use ha_device.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"include_energy": map[string]any{
						"type":        "boolean",
						"description": "When true, adds an energy section listing power/energy sensors. Default false keeps the snapshot focused on presence/security/climate.",
					},
					"include_diagnostic": map[string]any{
						"type":        "boolean",
						"description": "When true, includes entities marked diagnostic or config in the scan. Default false.",
					},
					"include_hidden": map[string]any{
						"type":        "boolean",
						"description": "When true, includes hidden-but-enabled entities. Default false keeps the snapshot aligned with default HA visibility.",
					},
					"max_per_section": map[string]any{
						"type":        "integer",
						"description": "Cap on each section. Default 12. Overflow is reported via <section>_truncated_count.",
					},
					"include": tools.EntityMetadataIncludeParameter(),
				},
			},
			Handler: a.handleHomeSnapshot,
		},
	}
}

func (a *HomeSnapshotTools) handleHomeSnapshot(ctx context.Context, args map[string]any) (string, error) {
	var req HomeSnapshotRequest
	if v, ok := args["include_energy"].(bool); ok {
		req.IncludeEnergy = v
	}
	if v, ok := args["include_diagnostic"].(bool); ok {
		req.IncludeDiagnostic = v
	}
	if v, ok := args["include_hidden"].(bool); ok {
		req.IncludeHidden = v
	}
	if v, err := optionalIntArg(args, "max_per_section"); err != nil {
		return "", err
	} else if v != nil {
		req.MaxPerSection = *v
	}
	include, err := tools.ParseEntityMetadataIncludesArg(args["include"], "include")
	if err != nil {
		return "", err
	}
	req.Include = include

	result, err := ComputeHomeSnapshot(ctx, a.client, req, time.Now())
	if err != nil {
		a.logger.Warn("ha_home_snapshot failed", "error", err)
		return "", err
	}
	return result, nil
}
