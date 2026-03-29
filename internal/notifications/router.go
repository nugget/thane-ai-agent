package notifications

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ChannelActivity describes a channel with recent interaction for a
// specific contact.
type ChannelActivity struct {
	Channel    string    // provider name: "signal", "owu"
	Contact    string    // resolved contact name
	LastActive time.Time // when the channel last had activity
}

// ChannelActivitySource provides information about which channels
// have recent activity for which contacts. Used by the notification
// router to prefer channels the user is actively engaged on.
type ChannelActivitySource interface {
	ActiveChannels() []ChannelActivity
}

// NotificationRouter selects a notification provider based on contact
// facts and channel activity, then orchestrates delivery for both
// fire-and-forget and actionable notifications. It is the single
// entry point for the provider-agnostic send_notification and
// request_human_decision tools.
type NotificationRouter struct {
	providers map[string]NotificationProvider
	contacts  ContactResolver
	records   *RecordStore
	activity  ChannelActivitySource
	logger    *slog.Logger
}

// NewNotificationRouter creates a router with contact resolution and
// optional record tracking for actionable notifications. A nil logger
// defaults to [slog.Default].
func NewNotificationRouter(contacts ContactResolver, records *RecordStore, logger *slog.Logger) *NotificationRouter {
	if logger == nil {
		logger = slog.Default()
	}
	return &NotificationRouter{
		providers: make(map[string]NotificationProvider),
		contacts:  contacts,
		records:   records,
		logger:    logger,
	}
}

// SetActivitySource configures channel activity awareness. When set,
// [Route] prefers providers with recent activity for the recipient.
func (r *NotificationRouter) SetActivitySource(src ChannelActivitySource) {
	r.activity = src
}

// RegisterProvider adds a notification provider to the router. Nil
// providers and providers with empty names are rejected. Registering a
// provider whose name already exists overwrites the previous one and
// logs a warning.
func (r *NotificationRouter) RegisterProvider(p NotificationProvider) {
	if p == nil {
		r.logger.Error("attempted to register nil notification provider")
		return
	}
	name := p.Name()
	if name == "" {
		r.logger.Error("attempted to register notification provider with empty name")
		return
	}
	if _, exists := r.providers[name]; exists {
		r.logger.Warn("overwriting existing notification provider", "name", name)
	}
	r.providers[name] = p
}

// Route resolves a recipient to the appropriate provider based on
// contact properties. It checks for an explicit notification_preference
// property first, then falls back to checking for known delivery channels.
func (r *NotificationRouter) Route(recipient string) (NotificationProvider, error) {
	contact, err := r.contacts.ResolveContact(recipient)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("contact %q not found", recipient)
		}
		return nil, fmt.Errorf("resolve contact %q: %w", recipient, err)
	}

	props, err := r.contacts.GetPropertiesMap(contact.ID)
	if err != nil {
		return nil, fmt.Errorf("lookup properties for %q: %w", recipient, err)
	}

	// 1. Explicit notification preference.
	if prefs, ok := props["notification_preference"]; ok && len(prefs) > 0 {
		if p, exists := r.providers[prefs[0]]; exists {
			return p, nil
		}
		r.logger.Warn("notification_preference references unknown provider",
			"recipient", recipient, "preference", prefs[0])
	}

	// 2. Active channel — prefer a provider the contact is currently
	// engaged on. Pick the most recently active channel that has a
	// registered provider.
	if r.activity != nil {
		var bestProvider NotificationProvider
		var bestTime time.Time
		contactName := contact.FormattedName
		for _, ch := range r.activity.ActiveChannels() {
			if !strings.EqualFold(ch.Contact, contactName) {
				continue
			}
			if ch.LastActive.After(bestTime) {
				if p, exists := r.providers[ch.Channel]; exists {
					bestProvider = p
					bestTime = ch.LastActive
				}
			}
		}
		if bestProvider != nil {
			r.logger.Info("routing to active channel",
				"recipient", recipient,
				"provider", bestProvider.Name(),
				"last_active", bestTime,
			)
			return bestProvider, nil
		}
	}

	// 3. Static fallback — HA companion app available → ha_push.
	if apps, ok := props["ha_companion_app"]; ok && len(apps) > 0 {
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
	if len(req.Actions) == 0 {
		return "", fmt.Errorf("actionable notification requires at least one action")
	}
	if req.Timeout <= 0 {
		return "", fmt.Errorf("actionable notification requires a positive timeout, got %s", req.Timeout)
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
