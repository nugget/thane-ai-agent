package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

// CallbackDispatcher handles MQTT messages on the thane/callbacks
// topic. It parses THANE_ action strings, looks up the notification
// record, marks it as responded, and routes the response to the
// appropriate handler (session injection or delegate).
type CallbackDispatcher struct {
	records  *RecordStore
	injector SessionInjector
	delegate DelegateSpawner
	logger   *slog.Logger
}

// NewCallbackDispatcher creates a callback dispatcher.
func NewCallbackDispatcher(records *RecordStore, injector SessionInjector, delegate DelegateSpawner, logger *slog.Logger) *CallbackDispatcher {
	return &CallbackDispatcher{
		records:  records,
		injector: injector,
		delegate: delegate,
		logger:   logger,
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

	if !strings.HasPrefix(p.Action, "THANE_") {
		d.logger.Debug("callback: ignoring non-THANE action", "action", p.Action)
		return
	}

	requestID, actionID, ok := parseCallbackAction(p.Action)
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

	if err := d.records.Respond(requestID, actionID); err != nil {
		d.logger.Error("callback: failed to mark responded",
			"request_id", requestID, "error", err)
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
// session is no longer alive. This method is also used by the timeout
// watcher to dispatch auto-fired timeout actions.
func (d *CallbackDispatcher) DispatchAction(record *Record, actionID string) {
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

	// Session is gone — spawn a delegate to handle the response.
	if d.delegate != nil {
		task := fmt.Sprintf(
			"Handle notification callback: user chose %q for notification %s to %s.",
			actionID, record.RequestID, record.Recipient,
		)
		if err := d.delegate.Spawn(context.Background(), task, msg); err != nil {
			d.logger.Error("callback: failed to spawn delegate",
				"request_id", record.RequestID,
				"error", err,
			)
		} else {
			d.logger.Info("callback: spawned delegate for expired session",
				"request_id", record.RequestID,
			)
		}
		return
	}

	d.logger.Warn("callback: no injector or delegate available, response lost",
		"request_id", record.RequestID,
		"action_id", actionID,
	)
}

// parseCallbackAction extracts the request ID and action ID from a
// callback action string formatted as THANE_{uuid}_{actionID}.
// UUIDv7 strings are exactly 36 characters (with hyphens).
func parseCallbackAction(action string) (requestID, actionID string, ok bool) {
	rest := strings.TrimPrefix(action, "THANE_")
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
