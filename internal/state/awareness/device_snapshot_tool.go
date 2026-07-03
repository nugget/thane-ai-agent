package awareness

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// DeviceSnapshotTools exposes the ha_device tool, an on-demand view of
// one Home Assistant device as a unit: its identity plus every child
// entity it exposes, grouped the way HA's device page groups them
// (Controls / Sensors / Configuration / Diagnostic) with an availability
// rollup. Implements [tools.Provider].
type DeviceSnapshotTools struct {
	client DeviceSnapshotClient
	logger *slog.Logger
}

// DeviceSnapshotToolsConfig captures the dependencies for
// [NewDeviceSnapshotTools]. Client is required.
type DeviceSnapshotToolsConfig struct {
	Client DeviceSnapshotClient
	Logger *slog.Logger
}

func NewDeviceSnapshotTools(cfg DeviceSnapshotToolsConfig) *DeviceSnapshotTools {
	if cfg.Client == nil {
		panic("awareness: DeviceSnapshotTools requires a non-nil Client")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &DeviceSnapshotTools{client: cfg.Client, logger: logger}
}

// Name implements [tools.Provider].
func (a *DeviceSnapshotTools) Name() string { return "awareness.ha_device" }

// Tools implements [tools.Provider].
func (a *DeviceSnapshotTools) Tools() []*tools.Tool {
	return []*tools.Tool{
		{
			Name: "ha_device",
			Description: "Get a full snapshot of one Home Assistant device as a unit — its identity " +
				"(manufacturer/model/firmware/serial/area/integration/via_device) and every child entity it exposes, " +
				"grouped exactly the way HA's device page groups them: controls (actionable primaries), sensors (read-only), " +
				"configuration (tuning knobs), and diagnostic (health counters), plus an availability rollup (how many entities are reporting). " +
				"Explicit inspection, so it also shows hidden entities (marked hidden:true) that the enumeration tools omit. " +
				"Use this to understand a physical device as a whole — 'show me the thermostat device', " +
				"'what does the front-door sensor expose', 'is this device healthy' — rather than fetching one entity at a time. " +
				"Resolves by device_id or by name (user-assigned or registry name, with substring fallback; returns candidates when ambiguous). " +
				"Add include for per-entity area/device/label/description/visibility metadata.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"device": map[string]any{
						"type":        "string",
						"description": "The device to inspect. Accepts the device_id, the user-assigned name, or the registry name (substring match falls back when there is no exact hit).",
					},
					"include": tools.EntityMetadataIncludeParameter(),
				},
				"required": []string{"device"},
			},
			Handler: a.handleDevice,
		},
	}
}

func (a *DeviceSnapshotTools) handleDevice(ctx context.Context, args map[string]any) (string, error) {
	device, _ := args["device"].(string)
	if device == "" {
		return "", fmt.Errorf("device is required")
	}

	include, err := tools.ParseEntityMetadataIncludesArg(args["include"], "include")
	if err != nil {
		return "", err
	}

	req := DeviceSnapshotRequest{Device: device, Include: include}
	result, err := ComputeDeviceSnapshot(ctx, a.client, req, time.Now())
	if err != nil {
		a.logger.Warn("ha_device failed",
			"device", device,
			"error", err,
		)
		return "", err
	}
	return result, nil
}
