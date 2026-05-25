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
			Description: "List all MQTT wake subscriptions. Each entry shows the topic filter, the source (config or runtime), and the wake_loop target that receives matching messages as event-source notifications.",
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
			Description: `Add a runtime MQTT wake subscription. Matching messages are delivered as event-source notifications to the loop named in wake_loop; the target loop's next iteration sees the message in its pending notifications and runs under its own Spec.Profile. Use this for "topic X → metacog", "topic Y → research_watcher", and similar attention-routing patterns. If you don't have a custom handler loop in mind, point wake_loop at the built-in mqtt-default-handler.

The subscription is persisted and a live SUBSCRIBE is sent to the broker; if the broker is not currently connected it activates on the next reconnect.

Topic conventions: Use thane/{device_name}/wake/{purpose} for instance-directed wakes. Subscribe to external topics directly for shared events (e.g., frigate/+/events). MQTT wildcards: + matches one level, # matches remaining levels (must be last segment).`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"topic": map[string]any{
						"type":        "string",
						"description": "MQTT topic filter to subscribe to (supports + and # wildcards).",
					},
					"wake_loop": messages.LoopWakeTargetSchema("Existing loop to receive matching messages as event-source notifications. Resolves at message-arrival time; verify the target exists via loop_status before persisting."),
				},
				"required": []string{"topic", "wake_loop"},
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
