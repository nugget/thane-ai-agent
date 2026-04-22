package mqtt

import (
	"context"

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
			Description: "List all MQTT wake subscriptions. Shows topic filters that trigger agent conversations when messages arrive, along with their routing configuration (mission, quality floor, model) and source (config or runtime).",
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
			Description: `Add a runtime MQTT wake subscription. When a message arrives on the given topic, the agent wakes with the specified routing configuration. The subscription is persisted and a live SUBSCRIBE is sent to the broker; if the broker is not currently connected it activates on the next reconnect. Persists across restarts.

Topic conventions: Use thane/{device_name}/wake/{purpose} for instance-directed wakes. Subscribe to external topics directly for shared events (e.g., frigate/+/events). MQTT wildcards: + matches one level, # matches remaining levels (must be last segment). Each wake creates a fresh conversation — no state accumulates across messages. The MQTT payload becomes the user message, optionally wrapped with the instructions field.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"topic": map[string]any{
						"type":        "string",
						"description": "MQTT topic filter to subscribe to (supports + and # wildcards)",
					},
					"model": map[string]any{
						"type":        "string",
						"description": "Explicit model name (bypasses router). Leave empty for router-based selection.",
					},
					"quality_floor": map[string]any{
						"type":        "string",
						"description": "Minimum model quality rating 1-10 (default: router decides)",
					},
					"mission": map[string]any{
						"type":        "string",
						"description": "Task context for routing: conversation, automation, device_control, background",
					},
					"local_only": map[string]any{
						"type":        "string",
						"description": "Restrict to free/local models: true or false",
					},
					"delegation_gating": map[string]any{
						"type":        "string",
						"description": "Delegation tool gating: enabled or disabled",
					},
					"prefer_speed": map[string]any{
						"type":        "string",
						"description": "Favour faster models: true or false",
					},
					"instructions": map[string]any{
						"type":        "string",
						"description": "Instructions injected into the wake message to guide the agent's behaviour",
					},
					"exclude_tools": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Tool names to exclude from the wake conversation",
					},
					"initial_tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Capability tags to activate at wake start",
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
					"id": map[string]any{
						"type":        "string",
						"description": "Subscription ID to remove (from mqtt_wake_list)",
					},
				},
				"required": []string{"id"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return w.tools.HandleRemoveWakeSubscription(ctx, args)
			},
		},
	}
}
