package mqtt

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// WakeTools is the [tools.Provider] for the mqtt_wake_list,
// mqtt_wake_add, and mqtt_wake_remove tools. It wraps [Tools] so the
// same handler implementations are exposed through the uniform
// provider contract.
type WakeTools struct {
	tools *Tools
}

// NewWakeTools constructs the provider. The caller owns the Tools
// value; typical wiring is:
//
//	reg.RegisterProvider(mqtt.NewWakeTools(mqtt.NewTools(subStore)))
func NewWakeTools(t *Tools) *WakeTools {
	return &WakeTools{tools: t}
}

// Name implements [tools.Provider].
func (w *WakeTools) Name() string { return "mqtt.wake" }

// Tools implements [tools.Provider].
func (w *WakeTools) Tools() []*tools.Tool {
	return []*tools.Tool{
		{
			Name:        "mqtt_wake_list",
			Description: "List all MQTT wake subscriptions. Each entry shows the topic filter, the source (config or runtime), and either wake_loop (modern target-dispatch shape — points at an existing loop that receives messages as event-source notifications) or the legacy routing fields (mission, quality_floor, model) for subscriptions that still spawn a one-shot loop per message.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return w.tools.HandleListWakeSubscriptions(ctx, args)
			},
		},
		{
			Name: "mqtt_wake_add",
			Description: `Add a runtime MQTT wake subscription. Two dispatch shapes are supported per-subscription:

  - **wake_loop (preferred):** messages on the topic are delivered as event-source notifications to an existing loop. The target loop's next iteration sees the message in its pending notifications and runs under its own Spec.Profile. No new loop is spawned; the registry stays clean. Use this for "topic X → metacog", "topic Y → research_watcher", and similar attention-routing patterns.

  - **Legacy spawn-per-message:** when wake_loop is omitted, the routing fields (model, mission, quality_floor, etc.) shape a fresh one-shot loop that runs on each matching message. Kept for backwards compatibility; new subscriptions should use wake_loop and let the target loop's own profile govern routing.

The subscription is persisted and a live SUBSCRIBE is sent to the broker; if the broker is not currently connected it activates on the next reconnect.

Topic conventions: Use thane/{device_name}/wake/{purpose} for instance-directed wakes. Subscribe to external topics directly for shared events (e.g., frigate/+/events). MQTT wildcards: + matches one level, # matches remaining levels (must be last segment).`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"topic": map[string]any{
						"type":        "string",
						"description": "MQTT topic filter to subscribe to (supports + and # wildcards).",
					},
					"wake_loop": messages.LoopWakeTargetSchema("Preferred dispatch: existing loop to receive matching messages as event-source notifications. Resolves at message-arrival time; verify the target exists via loop_status before persisting."),
					"model": map[string]any{
						"type":        "string",
						"description": "Legacy spawn-dispatch only: explicit model name (bypasses router). Ignored when wake_loop is set.",
					},
					"quality_floor": map[string]any{
						"type":        "integer",
						"description": "Legacy spawn-dispatch only: minimum model quality rating 1-10. Ignored when wake_loop is set.",
					},
					"mission": map[string]any{
						"type":        "string",
						"description": "Legacy spawn-dispatch only: task context (conversation, automation, device_control, background). Ignored when wake_loop is set.",
					},
					"local_only": map[string]any{
						"type":        "string",
						"description": "Legacy spawn-dispatch only: restrict to free/local models (true/false). Ignored when wake_loop is set.",
					},
					"delegation_gating": map[string]any{
						"type":        "string",
						"description": "Legacy spawn-dispatch only: delegation tool gating (enabled/disabled). Ignored when wake_loop is set.",
					},
					"prefer_speed": map[string]any{
						"type":        "string",
						"description": "Legacy spawn-dispatch only: favour faster models (true/false). Ignored when wake_loop is set.",
					},
					"instructions": map[string]any{
						"type":        "string",
						"description": "Legacy spawn-dispatch only: instructions injected into the spawned wake message. For wake_loop dispatch, set wake_loop.instructions instead.",
					},
					"exclude_tools": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Legacy spawn-dispatch only: tool names to exclude. Ignored when wake_loop is set (target loop's ExcludeTools governs).",
					},
					"initial_tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Legacy spawn-dispatch only: capability tags to activate. Ignored when wake_loop is set (target loop's tags govern).",
					},
				},
				"required": []string{"topic"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return w.tools.HandleAddWakeSubscription(ctx, args)
			},
		},
		{
			Name:        "mqtt_wake_remove",
			Description: "Remove a runtime MQTT wake subscription by ID. Config-defined subscriptions cannot be removed. Use mqtt_wake_list to find subscription IDs.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"subscription_id": map[string]any{
						"type":        "string",
						"description": "Subscription ID to remove (from mqtt_wake_list's subscription_id field).",
					},
				},
				"required": []string{"subscription_id"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return w.tools.HandleRemoveWakeSubscription(ctx, args)
			},
		},
	}
}
