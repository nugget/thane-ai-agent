package tools

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/notifications"
)

// SetHANotifier adds the ha_notify tool to the registry.
func (r *Registry) SetHANotifier(s *notifications.Sender) {
	r.notifier = s
	r.registerNotificationTools()
}

func (r *Registry) registerNotificationTools() {
	if r.notifier == nil {
		return
	}

	r.Register(&Tool{
		Name: "ha_notify",
		Description: "Send a push notification to a person's phone via Home Assistant companion app. " +
			"Use for informing the user about events, status updates, or anything that " +
			"needs their attention. This is the HA companion app channel specifically — " +
			"use signal_send_message for Signal delivery.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"recipient": map[string]any{
					"type":        "string",
					"description": "Contact name of the notification recipient",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Notification body text",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Notification title (optional)",
				},
				"priority": map[string]any{
					"type":        "string",
					"enum":        []string{"low", "normal", "urgent"},
					"description": "Notification priority: low (passive/FYI), normal (default), urgent (needs attention)",
				},
			},
			"required": []string{"recipient", "message"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			recipient, _ := args["recipient"].(string)
			message, _ := args["message"].(string)
			if recipient == "" {
				return "", fmt.Errorf("recipient is required")
			}
			if message == "" {
				return "", fmt.Errorf("message is required")
			}

			n := notifications.Notification{
				Recipient: recipient,
				Message:   message,
			}
			if title, ok := args["title"].(string); ok {
				n.Title = title
			}
			if priority, ok := args["priority"].(string); ok {
				n.Priority = priority
			}

			if err := r.notifier.Send(ctx, n); err != nil {
				return "", err
			}
			return fmt.Sprintf("Notification sent to %s", recipient), nil
		},
	})
}
