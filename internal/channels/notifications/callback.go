package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// SessionInjector injects a system message into a live conversation.
// Implementations are wired in main.go to avoid import cycles.
type SessionInjector interface {
	// InjectSystemMessage adds a system message to the conversation's
	// memory. The message will be visible on the next agent turn.
	InjectSystemMessage(conversationID, message string) error
	// IsSessionAlive reports whether a conversation has an active
	// archive session.
	IsSessionAlive(conversationID string) bool
}

// DelegateSpawner executes a task in a lightweight delegate loop when
// the originating session is no longer alive.
type DelegateSpawner interface {
	Spawn(ctx context.Context, task, guidance string) error
}

// ActionPrefix returns the action string prefix derived from a device
// name. Hyphens are replaced with underscores and the result is
// uppercased, e.g. "aimee-thane" becomes "AIMEE_THANE". An empty
// deviceName falls back to "THANE".
func ActionPrefix(deviceName string) string {
	if deviceName == "" {
		return "THANE"
	}
	return strings.ToUpper(strings.ReplaceAll(deviceName, "-", "_"))
}

// CallbackDispatcher handles MQTT messages on the instance-specific
// callbacks topic. It parses action strings with the configured prefix,
// looks up the notification record, marks it as responded, and routes
// the response to the appropriate handler (session injection or delegate).
type CallbackDispatcher struct {
	records      *RecordStore
	injector     SessionInjector
	delegate     DelegateSpawner
	waiter       *ResponseWaiter // optional; signals synchronous escalation waiters
	logger       *slog.Logger
	actionPrefix string // e.g., "AIMEE_THANE"
}

// SetResponseWaiter configures synchronous escalation support. When
// set, [DispatchAction] signals any waiting escalation tool in
// addition to injecting session messages or spawning delegates.
func (d *CallbackDispatcher) SetResponseWaiter(w *ResponseWaiter) {
	d.waiter = w
}

// ResponseWaiter returns the configured waiter, or nil.
func (d *CallbackDispatcher) ResponseWaiter() *ResponseWaiter {
	return d.waiter
}

// NewCallbackDispatcher creates a callback dispatcher. The deviceName
// parameter is used to derive the action prefix via [ActionPrefix].
func NewCallbackDispatcher(records *RecordStore, injector SessionInjector, delegate DelegateSpawner, deviceName string, logger *slog.Logger) *CallbackDispatcher {
	return &CallbackDispatcher{
		records:      records,
		injector:     injector,
		delegate:     delegate,
		logger:       logger,
		actionPrefix: ActionPrefix(deviceName),
	}
}

// callbackPayload is the JSON structure published by the HA automation.
type callbackPayload struct {
	Action    string `json:"action"`
	Timestamp string `json:"timestamp"`
}

// Handle is an mqtt.MessageHandler that processes callback messages
// published by the HA automation when a user taps a notification
// action button.
func (d *CallbackDispatcher) Handle(_ string, payload []byte) {
	var p callbackPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		d.logger.Warn("callback: invalid JSON payload", "error", err)
		return
	}

	prefix := d.actionPrefix + "_"
	if !strings.HasPrefix(p.Action, prefix) {
		d.logger.Debug("callback: ignoring action with wrong prefix",
			"action", p.Action, "expected_prefix", d.actionPrefix)
		return
	}

	requestID, actionID, ok := parseCallbackAction(p.Action, d.actionPrefix)
	if !ok {
		d.logger.Warn("callback: failed to parse action string", "action", p.Action)
		return
	}

	record, err := d.records.Get(requestID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			d.logger.Warn("callback: unknown request ID", "request_id", requestID)
		} else {
			d.logger.Error("callback: failed to look up record", "request_id", requestID, "error", err)
		}
		return
	}

	if record.Status != StatusPending {
		d.logger.Debug("callback: record not pending, ignoring",
			"request_id", requestID, "status", record.Status)
		return
	}

	// Validate that actionID is one of the declared actions.
	if !isValidAction(record.Actions, actionID) {
		d.logger.Warn("callback: unknown action ID for record",
			"request_id", requestID, "action_id", actionID)
		return
	}

	updated, err := d.records.Respond(requestID, actionID)
	if err != nil {
		d.logger.Error("callback: failed to mark responded",
			"request_id", requestID, "error", err)
		return
	}
	if !updated {
		// Lost the race — another callback or timeout already handled this.
		d.logger.Debug("callback: record no longer pending after Respond",
			"request_id", requestID)
		return
	}

	d.logger.Info("callback: user responded to notification",
		"request_id", requestID,
		"action_id", actionID,
		"recipient", record.Recipient,
	)

	d.DispatchAction(record, actionID)
}

// DispatchAction routes a notification response to the originating
// session (via system message injection) or spawns a delegate if the
// session is no longer alive. Delegate spawning is offloaded to a
// goroutine so it does not block the MQTT handler. This method is
// also used by the timeout watcher to dispatch auto-fired timeout
// actions.
func (d *CallbackDispatcher) DispatchAction(record *Record, actionID string) {
	// Signal any synchronous escalation waiter first. This unblocks
	// the request_human_escalation tool if it's waiting.
	if d.waiter != nil {
		if d.waiter.Signal(record.RequestID, actionID) {
			d.logger.Info("callback: signaled synchronous escalation waiter",
				"request_id", record.RequestID,
				"action_id", actionID,
			)
		}
	}

	msg := fmt.Sprintf(
		"User responded to notification %s: chose %q. Context: %s",
		record.RequestID, actionID, record.Context,
	)

	if record.OriginConversation != "" && d.injector != nil && d.injector.IsSessionAlive(record.OriginConversation) {
		if err := d.injector.InjectSystemMessage(record.OriginConversation, msg); err != nil {
			d.logger.Error("callback: failed to inject system message",
				"conversation_id", record.OriginConversation,
				"error", err,
			)
		} else {
			d.logger.Info("callback: injected system message into live session",
				"conversation_id", record.OriginConversation,
				"request_id", record.RequestID,
			)
		}
		return
	}

	// Session is gone — spawn a delegate in a background goroutine so
	// the MQTT handler is not blocked by the delegate loop execution.
	if d.delegate != nil {
		task := fmt.Sprintf(
			"Handle notification callback: user chose %q for notification %s to %s.",
			actionID, record.RequestID, record.Recipient,
		)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := d.delegate.Spawn(ctx, task, msg); err != nil {
				d.logger.Error("callback: delegate spawn failed",
					"request_id", record.RequestID,
					"error", err,
				)
			} else {
				d.logger.Info("callback: delegate completed for expired session",
					"request_id", record.RequestID,
				)
			}
		}()
		return
	}

	d.logger.Warn("callback: no injector or delegate available, response lost",
		"request_id", record.RequestID,
		"action_id", actionID,
	)
}

// isValidAction reports whether actionID matches one of the record's
// declared action IDs.
func isValidAction(actions []Action, actionID string) bool {
	for _, a := range actions {
		if a.ID == actionID {
			return true
		}
	}
	return false
}

// parseCallbackAction extracts the request ID and action ID from a
// callback action string formatted as {prefix}_{uuid}_{actionID}.
// The caller provides the prefix (e.g., "AIMEE_THANE"); this function
// strips it plus the trailing underscore, then parses the UUID (36
// characters with hyphens) and the action ID.
//
// The prefix check intentionally overlaps with Handle's prefix guard
// so that parseCallbackAction is self-contained and safe to call from
// tests or other entry points without relying on prior validation.
func parseCallbackAction(action, prefix string) (requestID, actionID string, ok bool) {
	full := prefix + "_"
	if !strings.HasPrefix(action, full) {
		return "", "", false
	}
	rest := action[len(full):]
	if len(rest) < 38 { // 36 (UUID) + 1 (_) + at least 1 char
		return "", "", false
	}
	requestID = rest[:36]
	if rest[36] != '_' {
		return "", "", false
	}
	actionID = rest[37:]
	if actionID == "" {
		return "", "", false
	}
	return requestID, actionID, true
}
