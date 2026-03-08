package notifications

import (
	"context"
	"log/slog"
	"time"
)

// TimeoutWatcher periodically checks for pending notification records
// whose expiry time has passed and executes their configured timeout
// action.
type TimeoutWatcher struct {
	records    *RecordStore
	dispatcher *CallbackDispatcher
	sender     *Sender
	interval   time.Duration
	logger     *slog.Logger
}

// NewTimeoutWatcher creates a timeout watcher. The sender parameter
// is optional and only needed for "escalate" timeout actions that
// re-send a notification at urgent priority.
func NewTimeoutWatcher(records *RecordStore, dispatcher *CallbackDispatcher, sender *Sender, interval time.Duration, logger *slog.Logger) *TimeoutWatcher {
	return &TimeoutWatcher{
		records:    records,
		dispatcher: dispatcher,
		sender:     sender,
		interval:   interval,
		logger:     logger,
	}
}

// Start runs the timeout watcher loop until ctx is cancelled. It
// checks for expired records at the configured interval.
func (w *TimeoutWatcher) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.check(ctx)
		}
	}
}

// check queries for pending expired records and handles each one.
func (w *TimeoutWatcher) check(ctx context.Context) {
	expired, err := w.records.PendingExpired()
	if err != nil {
		w.logger.Error("timeout watcher: failed to query expired records", "error", err)
		return
	}

	for _, rec := range expired {
		if err := w.records.Expire(rec.RequestID); err != nil {
			w.logger.Error("timeout watcher: failed to expire record",
				"request_id", rec.RequestID, "error", err)
			continue
		}

		w.logger.Info("timeout watcher: record expired",
			"request_id", rec.RequestID,
			"timeout_action", rec.TimeoutAction,
			"recipient", rec.Recipient,
		)

		w.handleTimeoutAction(ctx, rec)
	}
}

// handleTimeoutAction executes the configured timeout action for an
// expired record.
func (w *TimeoutWatcher) handleTimeoutAction(ctx context.Context, rec *Record) {
	switch rec.TimeoutAction {
	case "", "cancel":
		// No further action needed.
		return

	case "escalate":
		if w.sender == nil {
			w.logger.Warn("timeout watcher: escalation requested but no sender configured",
				"request_id", rec.RequestID)
			return
		}
		n := Notification{
			Recipient: rec.Recipient,
			Message:   "ESCALATION: No response received for a previous notification. " + rec.Context,
			Priority:  "urgent",
		}
		if err := w.sender.Send(ctx, n); err != nil {
			w.logger.Error("timeout watcher: failed to send escalation",
				"request_id", rec.RequestID, "error", err)
		} else {
			w.logger.Info("timeout watcher: escalation sent",
				"request_id", rec.RequestID, "recipient", rec.Recipient)
		}

	default:
		// Treat as an action ID — dispatch as if the user chose it.
		w.dispatcher.DispatchAction(rec, rec.TimeoutAction)
	}
}
