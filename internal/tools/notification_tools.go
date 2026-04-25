package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/channels/notifications"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
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
	r.logHANotify(ctx, n)
	return fmt.Sprintf("Notification sent to %s", recipient), nil
}

// handleActionableNotify creates a tracked notification with callback
// routing. Requires the record store to be configured.
func (r *Registry) handleActionableNotify(ctx context.Context, n notifications.Notification, actions []notifications.Action, args map[string]any) (string, error) {
	if r.notifRecords == nil {
		return "", fmt.Errorf("actionable notifications are not configured (notification record store is nil)")
	}

	u, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate request ID: %w", err)
	}
	requestID := u.String()

	timeout := defaultNotificationTimeout
	if ts, ok := args["timeout"].(string); ok && ts != "" {
		parsed, err := time.ParseDuration(ts)
		if err != nil {
			return "", fmt.Errorf("invalid timeout %q: %w", ts, err)
		}
		if parsed <= 0 {
			return "", fmt.Errorf("timeout must be positive, got %s", parsed)
		}
		timeout = parsed
	}

	timeoutAction, _ := args["timeout_action"].(string)
	notifContext, _ := args["context"].(string)

	// Set actionable fields on the notification for sender.
	n.Actions = actions
	n.RequestID = requestID
	n.Timeout = timeout
	n.TimeoutAction = timeoutAction
	n.Context = notifContext

	// Send first — if delivery fails, we don't create a dangling
	// record that could trigger timeout actions even though the user
	// never received the notification.
	if err := r.notifier.Send(ctx, n); err != nil {
		return "", err
	}

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
		Channel:            "ha_push",
		Source:             NotificationSource(ctx),
		Kind:               notifications.KindActionable,
		Title:              n.Title,
		Message:            n.Message,
	}
	if err := r.notifRecords.Create(rec); err != nil {
		return "", fmt.Errorf("create notification record: %w", err)
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

// CallbackDispatcher routes notification responses to originating
// sessions. Implemented by notifications.CallbackDispatcher.
type CallbackDispatcher interface {
	DispatchAction(record *notifications.Record, actionID string)
}

// SetNotificationRouter adds the provider-agnostic send_notification
// and request_human_decision tools to the registry.
func (r *Registry) SetNotificationRouter(router *notifications.NotificationRouter) {
	r.notifRouter = router
	r.registerGenericNotificationTools()
}

// SetCallbackDispatcher configures callback routing for the
// resolve_actionable tool.
func (r *Registry) SetCallbackDispatcher(d CallbackDispatcher) {
	r.notifDispatcher = d
	r.registerResolveActionable()
}

func (r *Registry) registerGenericNotificationTools() {
	if r.notifRouter == nil {
		return
	}

	r.Register(&Tool{
		Name: "send_notification",
		Description: "Send a notification to a person via the configured delivery channel " +
			"(currently Home Assistant push; additional channels may be added in the " +
			"future). The system selects the target using the recipient's contact facts. " +
			"Use this for informing people about events, updates, or anything that needs " +
			"their attention. This is fire-and-forget — no response tracking.",
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
		Handler: r.handleSendNotification,
	})

	r.Register(&Tool{
		Name: "request_human_decision",
		Description: "Request a decision from a person via an actionable notification with " +
			"response buttons (currently delivered via Home Assistant push; additional " +
			"channels may be added in the future). Creates a tracked request with " +
			"callback routing — you will receive a callback when they respond or on " +
			"timeout. The system selects the delivery channel using the recipient's " +
			"contact facts.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"recipient": map[string]any{
					"type":        "string",
					"description": "Contact name of the notification recipient",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Notification body text explaining what decision is needed",
				},
				"actions": map[string]any{
					"type":        "array",
					"description": "Action buttons presented to the recipient.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":    map[string]any{"type": "string", "description": "Short action identifier (e.g., \"approve\", \"deny\")"},
							"label": map[string]any{"type": "string", "description": "Button label shown to the user"},
						},
						"required": []string{"id", "label"},
					},
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
				"timeout": map[string]any{
					"type":        "string",
					"description": "How long to wait for a response (Go duration, e.g., \"30m\", \"1h\"). Default: 30m.",
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
			"required": []string{"recipient", "message", "actions"},
		},
		Handler: r.handleRequestHumanDecision,
	})
}

func (r *Registry) handleSendNotification(ctx context.Context, args map[string]any) (string, error) {
	recipient, _ := args["recipient"].(string)
	message, _ := args["message"].(string)
	if recipient == "" {
		return "", fmt.Errorf("recipient is required")
	}
	if message == "" {
		return "", fmt.Errorf("message is required")
	}

	req := notifications.NotificationRequest{
		Recipient: recipient,
		Message:   message,
	}
	if title, ok := args["title"].(string); ok {
		req.Title = title
	}
	if priority, ok := args["priority"].(string); ok {
		req.Priority = priority
	}

	if err := r.notifRouter.SendNotification(ctx, req); err != nil {
		return "", err
	}
	return fmt.Sprintf("Notification sent to %s", recipient), nil
}

func (r *Registry) handleRequestHumanDecision(ctx context.Context, args map[string]any) (string, error) {
	recipient, _ := args["recipient"].(string)
	message, _ := args["message"].(string)
	if recipient == "" {
		return "", fmt.Errorf("recipient is required")
	}
	if message == "" {
		return "", fmt.Errorf("message is required")
	}

	actions := parseActionsArg(args)
	if len(actions) == 0 {
		return "", fmt.Errorf("at least one action is required")
	}

	timeout := defaultNotificationTimeout
	if ts, ok := args["timeout"].(string); ok && ts != "" {
		parsed, err := time.ParseDuration(ts)
		if err != nil {
			return "", fmt.Errorf("invalid timeout %q: %w", ts, err)
		}
		if parsed <= 0 {
			return "", fmt.Errorf("timeout must be positive, got %s", parsed)
		}
		timeout = parsed
	}

	timeoutAction, _ := args["timeout_action"].(string)
	notifContext, _ := args["context"].(string)

	req := notifications.ActionableRequest{
		NotificationRequest: notifications.NotificationRequest{
			Recipient: recipient,
			Message:   message,
		},
		Actions:       actions,
		Timeout:       timeout,
		TimeoutAction: timeoutAction,
		Context:       notifContext,
	}
	if title, ok := args["title"].(string); ok {
		req.Title = title
	}
	if priority, ok := args["priority"].(string); ok {
		req.Priority = priority
	}

	requestID, err := r.notifRouter.SendActionable(
		ctx, req,
		SessionIDFromContext(ctx),
		ConversationIDFromContext(ctx),
	)
	if err != nil {
		return "", err
	}

	actionIDs := make([]string, len(actions))
	for i, a := range actions {
		actionIDs[i] = a.ID
	}

	return fmt.Sprintf(
		"Decision requested from %s with options [%s]. Request ID: %s. You will receive a callback when they respond or after %s timeout.",
		recipient, strings.Join(actionIDs, ", "), requestID, timeout,
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

// registerResolveActionable adds the resolve_actionable tool. Called
// when a CallbackDispatcher is configured.
func (r *Registry) registerResolveActionable() {
	if r.notifDispatcher == nil || r.notifRecords == nil {
		return
	}

	r.Register(&Tool{
		Name: "resolve_actionable",
		Description: "Resolve a pending actionable notification by recording the user's chosen action. " +
			"Use this when a user replies to an actionable notification in a conversational channel (e.g., Signal). " +
			"The notification's [request_id: ...] annotation in conversation history identifies which notification to resolve. " +
			"The action_id must match one of the notification's original action IDs.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"request_id": map[string]any{
					"type":        "string",
					"description": "The request_id from the notification's metadata annotation",
				},
				"action_id": map[string]any{
					"type":        "string",
					"description": "The action ID chosen by the user (must match one of the notification's original action IDs)",
				},
			},
			"required": []string{"request_id", "action_id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			requestID, _ := args["request_id"].(string)
			actionID, _ := args["action_id"].(string)
			if requestID == "" || actionID == "" {
				return "", fmt.Errorf("request_id and action_id are required")
			}

			// Validate the UUID format.
			if _, err := uuid.Parse(requestID); err != nil {
				return "", fmt.Errorf("invalid request_id %q: %w", requestID, err)
			}

			// Fetch the record.
			record, err := r.notifRecords.Get(requestID)
			if err != nil {
				return "", fmt.Errorf("notification %s not found: %w", requestID, err)
			}

			// Validate the action ID against the record's actions.
			validAction := false
			for _, a := range record.Actions {
				if a.ID == actionID {
					validAction = true
					break
				}
			}
			if !validAction {
				validIDs := make([]string, len(record.Actions))
				for i, a := range record.Actions {
					validIDs[i] = a.ID
				}
				return "", fmt.Errorf("invalid action_id %q; valid actions: %s",
					actionID, strings.Join(validIDs, ", "))
			}

			// Atomically mark as responded (race-safe with timeout watcher).
			ok, err := r.notifRecords.Respond(requestID, actionID)
			if err != nil {
				return "", fmt.Errorf("resolve notification %s: %w", requestID, err)
			}
			if !ok {
				return fmt.Sprintf("Notification %s already resolved.", requestID), nil
			}

			// Dispatch the callback to the originating session/conversation.
			r.notifDispatcher.DispatchAction(record, actionID)

			return fmt.Sprintf("Notification %s resolved: action %q dispatched to originating conversation.",
				requestID, actionID), nil
		},
	})
}

// NotificationSource builds a source identifier from the request
// context for notification history logging. Returns a string like
// "metacognitive", "signal/+15125551234", or "agent".
func NotificationSource(ctx context.Context) string {
	hints := HintsFromContext(ctx)
	if hints == nil {
		return "agent"
	}
	source := hints["source"]
	if source == "" {
		source = hints["channel"]
	}
	if source == "" {
		return "agent"
	}
	// For per-sender channels, append the sender identity.
	if sender := hints["sender"]; sender != "" {
		return source + "/" + sender
	}
	return source
}

// logHANotify records a fire-and-forget ha_notify send for history
// awareness. Errors are logged but not propagated — delivery already
// succeeded and logging failures should not surface as tool errors.
func (r *Registry) logHANotify(ctx context.Context, n notifications.Notification) {
	if r.notifRecords == nil {
		return
	}
	logger := logging.Logger(ctx)
	u, err := uuid.NewV7()
	if err != nil {
		logger.Warn("failed to generate notification log ID", "error", err)
		return
	}
	now := time.Now().UTC()
	if err := r.notifRecords.Log(&notifications.Record{
		RequestID: u.String(),
		Recipient: n.Recipient,
		Channel:   "ha_push",
		Source:    NotificationSource(ctx),
		Kind:      notifications.KindFireAndForget,
		Title:     n.Title,
		Message:   n.Message,
		CreatedAt: now,
	}); err != nil {
		logger.Warn("failed to log ha_notify for history", "error", err)
	}
}
