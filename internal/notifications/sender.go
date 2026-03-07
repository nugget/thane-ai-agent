// Package notifications delivers push notifications via Home Assistant companion apps.
package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/contacts"
)

const opstateNamespace = "channel:ha_notify"

// HAClient is the subset of homeassistant.Client needed for notifications.
type HAClient interface {
	CallService(ctx context.Context, domain, service string, data map[string]any) error
}

// ContactResolver resolves a contact name to its record and facts.
type ContactResolver interface {
	FindByName(name string) (*contacts.Contact, error)
	GetFacts(contactID uuid.UUID) (map[string][]string, error)
}

// OpstateStore is the subset of opstate.Store needed for recording notifications.
type OpstateStore interface {
	Set(namespace, key, value string) error
}

// Notification is a fire-and-forget push notification.
type Notification struct {
	Recipient string // contact name (e.g., "nugget")
	Title     string // notification title (optional)
	Message   string // notification body (required)
	Priority  string // "low", "normal" (default), "urgent"
}

// Sender delivers notifications via Home Assistant companion app push.
type Sender struct {
	ha       HAClient
	contacts ContactResolver
	opstate  OpstateStore
	logger   *slog.Logger
}

// NewSender creates a notification sender.
func NewSender(ha HAClient, contacts ContactResolver, opstate OpstateStore, logger *slog.Logger) *Sender {
	return &Sender{
		ha:       ha,
		contacts: contacts,
		opstate:  opstate,
		logger:   logger,
	}
}

// Send resolves the recipient to a HA companion app entity and sends
// a push notification via HA's notify service.
func (s *Sender) Send(ctx context.Context, n Notification) error {
	if n.Message == "" {
		return fmt.Errorf("notification message is required")
	}
	if n.Recipient == "" {
		return fmt.Errorf("notification recipient is required")
	}

	contact, err := s.contacts.FindByName(n.Recipient)
	if err != nil {
		return fmt.Errorf("contact %q not found: %w", n.Recipient, err)
	}

	facts, err := s.contacts.GetFacts(contact.ID)
	if err != nil {
		return fmt.Errorf("lookup facts for %q: %w", n.Recipient, err)
	}

	apps, ok := facts["ha_companion_app"]
	if !ok || len(apps) == 0 {
		return fmt.Errorf("contact %q has no ha_companion_app fact configured", n.Recipient)
	}
	entity := apps[0]

	data := map[string]any{
		"message": n.Message,
	}
	if n.Title != "" {
		data["title"] = n.Title
	}
	if pd := priorityData(n.Priority); pd != nil {
		data["data"] = pd
	}

	service := fmt.Sprintf("notify.%s", entity)
	s.logger.Info("sending notification",
		"recipient", n.Recipient,
		"service", service,
		"priority", n.Priority,
	)

	if err := s.ha.CallService(ctx, "notify", entity, data); err != nil {
		return fmt.Errorf("HA notify call failed: %w", err)
	}

	s.recordOpstate(n)

	return nil
}

// recordOpstate writes a send record to opstate for visibility by other loops.
func (s *Sender) recordOpstate(n Notification) {
	if s.opstate == nil {
		return
	}

	priority := n.Priority
	if priority == "" {
		priority = "normal"
	}

	ts := time.Now().Unix()
	key := fmt.Sprintf("%s:sent:%d", n.Recipient, ts)

	record := map[string]string{
		"source":   "agent",
		"priority": priority,
	}
	if n.Title != "" {
		record["title"] = n.Title
	}

	value, err := json.Marshal(record)
	if err != nil {
		s.logger.Warn("failed to marshal opstate record", "error", err)
		return
	}

	if err := s.opstate.Set(opstateNamespace, key, string(value)); err != nil {
		s.logger.Warn("failed to record notification in opstate", "error", err)
	}
}

// priorityData maps a priority level to HA notification push data.
func priorityData(priority string) map[string]any {
	switch priority {
	case "low":
		return map[string]any{"push": map[string]any{"interruption-level": "passive"}}
	case "urgent":
		return map[string]any{"push": map[string]any{"interruption-level": "time-sensitive"}}
	default:
		return nil
	}
}
