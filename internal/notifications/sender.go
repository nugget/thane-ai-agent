// Package notifications delivers push notifications via Home Assistant companion apps.
package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	SetWithTTL(namespace, key, value string, ttl time.Duration) error
}

// Notification is a push notification. Without Actions it is
// fire-and-forget (Phase 1). With Actions it creates an actionable
// notification with callback tracking (Phase 2).
type Notification struct {
	Recipient     string        // contact name (e.g., "nugget")
	Title         string        // notification title (optional)
	Message       string        // notification body (required)
	Priority      string        // "low", "normal" (default), "urgent"
	Actions       []Action      // optional: action buttons (creates tracked notification)
	RequestID     string        // UUIDv7, set by caller when Actions is non-empty
	Timeout       time.Duration // how long to wait for response (default: 30m)
	TimeoutAction string        // action ID to auto-execute on timeout, or "escalate"/"cancel"
	Context       string        // model-provided context for callback handling
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
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("contact %q not found", n.Recipient)
		}
		return fmt.Errorf("find contact %q: %w", n.Recipient, err)
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

	// Build the inner "data" sub-map, merging priority push settings
	// and action buttons when present.
	innerData := map[string]any{}
	if pd := priorityData(n.Priority); pd != nil {
		for k, v := range pd {
			innerData[k] = v
		}
	}
	if len(n.Actions) > 0 {
		if n.RequestID == "" {
			return fmt.Errorf("request_id is required when sending actionable notification")
		}
		innerData["actions"] = buildHAActions(n.RequestID, n.Actions)
	}
	if len(innerData) > 0 {
		data["data"] = innerData
	}

	logFields := []any{
		"recipient", n.Recipient,
		"domain", "notify",
		"service", entity,
		"priority", n.Priority,
	}
	if n.RequestID != "" {
		logFields = append(logFields, "request_id", n.RequestID)
	}
	if len(n.Actions) > 0 {
		logFields = append(logFields, "actions", len(n.Actions))
	}
	s.logger.Info("sending notification", logFields...)

	if err := s.ha.CallService(ctx, "notify", entity, data); err != nil {
		return fmt.Errorf("HA notify call failed: %w", err)
	}

	s.recordOpstate(n)

	return nil
}

// notifyRecordTTL is how long notification send records are kept in
// opstate. Records are only needed for short-term rate-limiting and
// dedup, not long-term history.
const notifyRecordTTL = 4 * time.Hour

// recordOpstate writes a send record to opstate for visibility by other loops.
// Records expire after [notifyRecordTTL].
func (s *Sender) recordOpstate(n Notification) {
	if s.opstate == nil {
		return
	}

	priority := n.Priority
	if priority == "" {
		priority = "normal"
	}

	ts := time.Now().UnixNano()
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

	if err := s.opstate.SetWithTTL(opstateNamespace, key, string(value), notifyRecordTTL); err != nil {
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

// buildHAActions creates the HA companion app action button definitions.
// Each action's callback string is formatted as THANE_{requestID}_{actionID}
// so the callback router can parse it back to the originating record.
func buildHAActions(requestID string, actions []Action) []map[string]any {
	haActions := make([]map[string]any, len(actions))
	for i, a := range actions {
		haActions[i] = map[string]any{
			"action": fmt.Sprintf("THANE_%s_%s", requestID, a.ID),
			"title":  a.Label,
		}
	}
	return haActions
}
