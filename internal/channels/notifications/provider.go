package notifications

import (
	"context"
	"time"
)

// NotificationProvider delivers notifications through a specific
// transport (e.g., HA companion app push, Signal, email).
type NotificationProvider interface {
	// Send delivers a fire-and-forget notification.
	Send(ctx context.Context, req NotificationRequest) error
	// SendActionable delivers a notification with action buttons.
	// The RequestID is pre-set by the router before calling the
	// provider; the provider only handles delivery.
	SendActionable(ctx context.Context, req ActionableRequest) error
	// Name returns the provider identifier (e.g., "ha_push").
	Name() string
}

// NotificationRequest is a fire-and-forget notification delivery
// request. It contains only the fields common to all providers.
type NotificationRequest struct {
	Recipient string // contact name (resolved by router)
	Title     string // optional
	Message   string // required
	Priority  string // "low", "normal", "urgent"
}

// ActionableRequest extends NotificationRequest with callback tracking
// fields for human-in-the-loop notifications.
type ActionableRequest struct {
	NotificationRequest
	Actions       []Action      // required, at least one
	RequestID     string        // UUIDv7, set by router
	Timeout       time.Duration // how long to wait for response
	TimeoutAction string        // action ID to auto-execute, "escalate", or "cancel"
	Context       string        // model-provided context for callback handling
}

// EscalationSender sends fire-and-forget notifications for timeout
// escalation. Both [*Sender] and [*NotificationRouter] implement this
// interface, allowing [TimeoutWatcher] to use either.
type EscalationSender interface {
	Send(ctx context.Context, n Notification) error
}
