package notifications

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// NotificationRouter selects a notification provider based on contact
// facts and orchestrates delivery for both fire-and-forget and
// actionable notifications. It is the single entry point for the
// provider-agnostic send_notification and request_human_decision tools.
type NotificationRouter struct {
	providers map[string]NotificationProvider
	contacts  ContactResolver
	records   *RecordStore
	logger    *slog.Logger
}

// NewNotificationRouter creates a router with contact resolution and
// optional record tracking for actionable notifications.
func NewNotificationRouter(contacts ContactResolver, records *RecordStore, logger *slog.Logger) *NotificationRouter {
	return &NotificationRouter{
		providers: make(map[string]NotificationProvider),
		contacts:  contacts,
		records:   records,
		logger:    logger,
	}
}

// RegisterProvider adds a notification provider to the router.
func (r *NotificationRouter) RegisterProvider(p NotificationProvider) {
	r.providers[p.Name()] = p
}

// Route resolves a recipient to the appropriate provider based on
// contact facts. It checks for an explicit notification_preference
// fact first, then falls back to checking for known delivery channels.
func (r *NotificationRouter) Route(recipient string) (NotificationProvider, error) {
	contact, err := r.contacts.ResolveContact(recipient)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("contact %q not found", recipient)
		}
		return nil, fmt.Errorf("resolve contact %q: %w", recipient, err)
	}

	facts, err := r.contacts.GetFacts(contact.ID)
	if err != nil {
		return nil, fmt.Errorf("lookup facts for %q: %w", recipient, err)
	}

	// 1. Explicit notification preference.
	if prefs, ok := facts["notification_preference"]; ok && len(prefs) > 0 {
		if p, exists := r.providers[prefs[0]]; exists {
			return p, nil
		}
		r.logger.Warn("notification_preference references unknown provider",
			"recipient", recipient, "preference", prefs[0])
	}

	// 2. HA companion app available → route to ha_push.
	if _, ok := facts["ha_companion_app"]; ok {
		if p, exists := r.providers["ha_push"]; exists {
			return p, nil
		}
	}

	return nil, fmt.Errorf("no notification provider available for contact %q", recipient)
}

// SendNotification delivers a fire-and-forget notification via the
// appropriate provider for the recipient.
func (r *NotificationRouter) SendNotification(ctx context.Context, req NotificationRequest) error {
	provider, err := r.Route(req.Recipient)
	if err != nil {
		return err
	}
	return provider.Send(ctx, req)
}

// SendActionable delivers an actionable notification and creates a
// tracking record. Returns the request ID for the created record.
// Delivery is attempted first; records are only created on success
// to avoid dangling records that trigger timeout actions for
// undelivered notifications.
func (r *NotificationRouter) SendActionable(ctx context.Context, req ActionableRequest, sessionID, conversationID string) (string, error) {
	if r.records == nil {
		return "", fmt.Errorf("actionable notifications are not configured (record store is nil)")
	}

	provider, err := r.Route(req.Recipient)
	if err != nil {
		return "", err
	}

	// Generate request ID.
	u, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate request ID: %w", err)
	}
	req.RequestID = u.String()

	// Deliver first — fail early before creating a tracking record.
	if err := provider.SendActionable(ctx, req); err != nil {
		return "", err
	}

	// Create tracking record.
	now := time.Now().UTC()
	rec := &Record{
		RequestID:          req.RequestID,
		Recipient:          req.Recipient,
		OriginSession:      sessionID,
		OriginConversation: conversationID,
		Context:            req.Context,
		Actions:            req.Actions,
		TimeoutSeconds:     int(req.Timeout.Seconds()),
		TimeoutAction:      req.TimeoutAction,
		CreatedAt:          now,
		ExpiresAt:          now.Add(req.Timeout),
	}
	if err := r.records.Create(rec); err != nil {
		return "", fmt.Errorf("create notification record: %w", err)
	}

	r.logger.Info("actionable notification routed",
		"recipient", req.Recipient,
		"provider", provider.Name(),
		"request_id", req.RequestID,
		"actions", len(req.Actions),
		"timeout", req.Timeout,
	)

	return req.RequestID, nil
}

// Send satisfies [EscalationSender] so the router can be used by
// [TimeoutWatcher] for escalation notifications. It adapts the legacy
// [Notification] struct to a [NotificationRequest] and routes it.
func (r *NotificationRouter) Send(ctx context.Context, n Notification) error {
	return r.SendNotification(ctx, NotificationRequest{
		Recipient: n.Recipient,
		Title:     n.Title,
		Message:   n.Message,
		Priority:  n.Priority,
	})
}
