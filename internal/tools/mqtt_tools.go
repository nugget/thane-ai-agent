package tools

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
)

// SetMQTTSubscriptionTools adds MQTT wake subscription management tools
// to the registry.
func (r *Registry) SetMQTTSubscriptionTools(mt *mqtt.Tools) {
	r.mqttSubTools = mt
	r.registerMQTTSubscriptionTools()
}

func (r *Registry) registerMQTTSubscriptionTools() {
	if r.mqttSubTools == nil {
		return
	}

	r.Register(&Tool{
		Name:        "mqtt_wake_list",
		Description: "List all MQTT wake subscriptions. Shows topic filters that trigger agent conversations when messages arrive, along with their routing configuration (mission, quality floor, model) and source (config or runtime).",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.mqttSubTools.HandleListWakeSubscriptions(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "mqtt_wake_add",
		Description: "Add a runtime MQTT wake subscription. When a message arrives on the given topic, the agent wakes with the specified routing configuration. Supports MQTT wildcards (+ for single level, # for multi-level). The subscription persists across restarts and takes effect on the next broker reconnect.",
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
				"seed_tags": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Capability tags to activate at wake start",
				},
			},
			"required": []string{"topic"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.mqttSubTools.HandleAddWakeSubscription(ctx, args)
		},
	})

	r.Register(&Tool{
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
			return r.mqttSubTools.HandleRemoveWakeSubscription(ctx, args)
		},
	})
}
