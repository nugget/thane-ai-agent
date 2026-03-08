package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/notifications"
)

// defaultNotificationTimeout is the default time to wait for a user
// response before executing the timeout action.
const defaultNotificationTimeout = 30 * time.Minute

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
			"use signal_send_message for Signal delivery.\n\n" +
			"For actionable notifications (requiring a response), supply the 'actions' array. " +
			"You will receive a callback when the user responds. Without 'actions', the notification is fire-and-forget.",
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
				"actions": map[string]any{
					"type":        "array",
					"description": "Action buttons for the notification. When provided, creates a tracked notification with callback routing.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":    map[string]any{"type": "string", "description": "Short action identifier (e.g., \"approve\", \"deny\")"},
							"label": map[string]any{"type": "string", "description": "Button label shown to the user"},
						},
						"required": []string{"id", "label"},
					},
				},
				"timeout": map[string]any{
					"type":        "string",
					"description": "How long to wait for a response (Go duration, e.g., \"30m\", \"1h\"). Default: 30m. Only used with actions.",
				},
				"timeout_action": map[string]any{
					"type":        "string",
					"description": "Action to take on timeout: an action ID to auto-execute, \"escalate\" to re-send at urgent priority, or \"cancel\" (default) to do nothing.",
				},
				"context": map[string]any{
					"type":        "string",
					"description": "Context for the callback handler explaining what to do with the response. Stored with the notification record.",
				},
			},
			"required": []string{"recipient", "message"},
		},
		Handler: r.handleHANotify,
	})
}

func (r *Registry) handleHANotify(ctx context.Context, args map[string]any) (string, error) {
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

	// Parse actions — if present, this becomes an actionable notification.
	actions := parseActionsArg(args)
	if len(actions) > 0 {
		return r.handleActionableNotify(ctx, n, actions, args)
	}

	// Fire-and-forget path (Phase 1).
	if err := r.notifier.Send(ctx, n); err != nil {
		return "", err
	}
	return fmt.Sprintf("Notification sent to %s", recipient), nil
}

// handleActionableNotify creates a tracked notification with callback
// routing. Requires the record store to be configured.
func (r *Registry) handleActionableNotify(ctx context.Context, n notifications.Notification, actions []notifications.Action, args map[string]any) (string, error) {
	if r.notifRecords == nil {
		return "", fmt.Errorf("actionable notifications are not configured (notification record store is nil)")
	}

	requestID := uuid.Must(uuid.NewV7()).String()

	timeout := defaultNotificationTimeout
	if ts, ok := args["timeout"].(string); ok && ts != "" {
		parsed, err := time.ParseDuration(ts)
		if err != nil {
			return "", fmt.Errorf("invalid timeout %q: %w", ts, err)
		}
		timeout = parsed
	}

	timeoutAction, _ := args["timeout_action"].(string)
	notifContext, _ := args["context"].(string)

	now := time.Now().UTC()
	rec := &notifications.Record{
		RequestID:          requestID,
		Recipient:          n.Recipient,
		OriginSession:      SessionIDFromContext(ctx),
		OriginConversation: ConversationIDFromContext(ctx),
		Context:            notifContext,
		Actions:            actions,
		TimeoutSeconds:     int(timeout.Seconds()),
		TimeoutAction:      timeoutAction,
		CreatedAt:          now,
		ExpiresAt:          now.Add(timeout),
	}
	if err := r.notifRecords.Create(rec); err != nil {
		return "", fmt.Errorf("create notification record: %w", err)
	}

	// Set actionable fields on the notification for sender.
	n.Actions = actions
	n.RequestID = requestID
	n.Timeout = timeout
	n.TimeoutAction = timeoutAction
	n.Context = notifContext

	if err := r.notifier.Send(ctx, n); err != nil {
		return "", err
	}

	actionIDs := make([]string, len(actions))
	for i, a := range actions {
		actionIDs[i] = a.ID
	}

	return fmt.Sprintf(
		"Notification sent to %s with actions [%s]. Request ID: %s. You will receive a callback when they respond or after %s timeout.",
		n.Recipient, strings.Join(actionIDs, ", "), requestID, timeout,
	), nil
}

// parseActionsArg extracts the actions array from tool arguments.
// Returns nil if no actions are provided or the format is invalid.
func parseActionsArg(args map[string]any) []notifications.Action {
	rawActions, ok := args["actions"]
	if !ok {
		return nil
	}

	actionSlice, ok := rawActions.([]any)
	if !ok || len(actionSlice) == 0 {
		return nil
	}

	var actions []notifications.Action
	for _, raw := range actionSlice {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		label, _ := m["label"].(string)
		if id == "" || label == "" {
			continue
		}
		actions = append(actions, notifications.Action{ID: id, Label: label})
	}
	return actions
}
