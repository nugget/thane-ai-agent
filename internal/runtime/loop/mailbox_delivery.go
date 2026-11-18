package loop

import (
	"context"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

// maxMailboxDeliveries is how many counted wake deliveries a mailbox
// item gets. The drain after the last one sets the item aside instead of
// delivering it again. Because a previously delivered item is only ever
// delivered alone, at most the first of those failures is shared with
// other items; the rest are the item's own.
const maxMailboxDeliveries = 3

// mailboxSettleTimeout bounds a post-turn mailbox write (ack or refund)
// that must land even when the loop is stopping.
const mailboxSettleTimeout = 5 * time.Second

// mailboxDelivery is one wake's mailbox batch.
type mailboxDelivery struct {
	// items is the batch handed to the turn, in drain order.
	items []MailboxItem
	// isolated is true when items is a single previously delivered
	// item, delivered without mid-turn input so its outcome is its own.
	isolated bool
	// counted is true when this delivery was recorded against the
	// items, so settling it may keep or refund the count.
	counted bool
}

// mailboxFailure is the last counted delivery failure this process saw,
// named by the wake batch's head item. Owned by the run goroutine.
type mailboxFailure struct {
	itemID string
	err    string
}

// planMailboxDelivery chooses a wake batch from pending items in drain
// order. Items at maxDeliveries are exhausted and returned for setting
// aside. Of the rest, a head that was delivered before goes alone
// (isolated); otherwise the batch is the leading run of items never
// delivered before.
func planMailboxDelivery(pending []MailboxItem, maxDeliveries int) (batch []MailboxItem, isolated bool, exhausted []MailboxItem) {
	live := make([]MailboxItem, 0, len(pending))
	for _, it := range pending {
		if it.PriorDeliveries >= maxDeliveries {
			exhausted = append(exhausted, it)
			continue
		}
		live = append(live, it)
	}
	if len(live) == 0 {
		return nil, false, exhausted
	}
	if live[0].PriorDeliveries > 0 {
		return live[:1], true, exhausted
	}
	n := 0
	for n < len(live) && live[n].PriorDeliveries == 0 {
		n++
	}
	return live[:n], false, exhausted
}

// deliverMailbox drains this wake's batch: it sets aside exhausted
// items, chooses the batch, and records the delivery before any turn
// code runs, so a process that dies during the turn still counts it.
// Store write failures fail open: an item that could not be set aside
// is withheld from this wake, and a batch whose count could not be
// written is delivered uncounted.
func (l *Loop) deliverMailbox(ctx context.Context, log *slog.Logger) (mailboxDelivery, error) {
	l.mu.Lock()
	mb, name := l.deps.Mailbox, l.config.Name
	l.mu.Unlock()
	if mb == nil {
		return mailboxDelivery{}, nil
	}
	for {
		pending, err := mb.Peek(ctx, name, maxMailboxItemsPerWake)
		if err != nil {
			return mailboxDelivery{}, err
		}
		batch, isolated, exhausted := planMailboxDelivery(pending, maxMailboxDeliveries)
		if len(exhausted) > 0 && l.setAsideMailboxItems(ctx, log, mb, name, exhausted) && len(batch) == 0 {
			continue // the window held only exhausted items; look past them
		}
		if len(batch) == 0 {
			return mailboxDelivery{}, nil
		}
		counted := true
		if err := mb.recordDelivery(ctx, name, batch); err != nil {
			counted = false
			log.Warn("loop mailbox delivery not recorded; delivering uncounted",
				"error", err,
				"items", len(batch),
			)
		}
		if isolated {
			log.Info("loop mailbox redelivering a previously failed item alone",
				"item_id", batch[0].ID,
				"delivery", batch[0].PriorDeliveries+1,
				"max_deliveries", maxMailboxDeliveries,
			)
		}
		return mailboxDelivery{items: batch, isolated: isolated, counted: counted}, nil
	}
}

// setAsideMailboxItems moves exhausted items out of the drain set and
// logs each. It reports whether any row changed.
func (l *Loop) setAsideMailboxItems(ctx context.Context, log *slog.Logger, mb *Mailbox, name string, items []MailboxItem) bool {
	changed, err := mb.putAside(ctx, name, items)
	if err != nil {
		log.Warn("loop mailbox set-aside not recorded; withholding the items from this wake",
			"error", err,
			"item_ids", mailboxItemIDs(items),
		)
		return false
	}
	now := time.Now()
	for _, it := range items {
		attrs := []any{
			"item_id", it.ID,
			"deliveries", it.PriorDeliveries,
			"max_deliveries", maxMailboxDeliveries,
			"waited", now.Sub(it.EnqueuedAt).Round(time.Second),
			"last_failure_seen_by_this_process", l.lastMailboxFailure.itemID == it.ID,
		}
		if l.lastMailboxFailure.itemID == it.ID {
			attrs = append(attrs, "last_error", l.lastMailboxFailure.err)
		}
		log.Warn("loop mailbox item set aside after repeated failed deliveries", attrs...)
	}
	if changed > 0 {
		l.clearErrorStreakOf(log, items)
	}
	return changed > 0
}

// clearErrorStreakOf ends the loop's error streak when the item that
// caused it was just set aside. Those failures were the item's, and the
// wake that sets it aside has nothing to run, so without this the loop
// would report errors until its next clean turn, which may be days away.
func (l *Loop) clearErrorStreakOf(log *slog.Logger, items []MailboxItem) {
	cause := l.lastMailboxFailure.itemID
	if cause == "" {
		return
	}
	for _, it := range items {
		if it.ID != cause {
			continue
		}
		l.mu.Lock()
		streak := l.consecutiveErrors
		l.consecutiveErrors = 0
		l.lastError = ""
		l.mu.Unlock()
		l.lastMailboxFailure = mailboxFailure{}
		log.Info("loop error streak cleared: the mailbox item behind it was set aside",
			"item_id", it.ID,
			"cleared_errors", streak,
		)
		return
	}
}

// settleFailedMailboxDelivery decides whether a failed turn's wake batch
// keeps its counted delivery, and reports whether it did. Only a
// TurnBuilder error keeps it: preparation is where an item is decoded,
// rendered and its attachments processed, so its failure is the item's.
// A stopping loop, and any failure after a turn was built (request
// preparation, the model run, the provider, the reply send) or inside a
// Handler, gives the delivery back: those failures say nothing about the
// items, and counting them would set aside healthy items during a
// provider outage. A process that dies keeps every count.
func (l *Loop) settleFailedMailboxDelivery(ctx context.Context, log *slog.Logger, delivery mailboxDelivery, turnErr error, builderFailed bool) bool {
	items := delivery.items
	reason := mailboxSettleReason(ctx.Err(), l.config.Handler != nil, builderFailed)
	attrs := []any{
		"reason", reason,
		"item_ids", mailboxItemIDs(items),
		"head_delivery", items[0].PriorDeliveries + 1,
		"max_deliveries", maxMailboxDeliveries,
	}
	if reason == "builder_failed" {
		l.lastMailboxFailure = mailboxFailure{itemID: items[0].ID, err: turnErr.Error()}
	}
	if !delivery.counted {
		// Nothing was recorded, so there is nothing to keep and refunding
		// would take back an earlier delivery's count.
		log.Info("loop mailbox delivery failed; the delivery was not counted", attrs...)
		return false
	}
	if reason == "builder_failed" {
		log.Info("loop mailbox delivery failed; delivery counted", attrs...)
		return true
	}
	l.mu.Lock()
	mb, name := l.deps.Mailbox, l.config.Name
	l.mu.Unlock()
	wctx, cancel := mailboxSettleContext(ctx)
	defer cancel()
	if err := mb.refundDelivery(wctx, name, items); err != nil {
		log.Warn("loop mailbox delivery refund failed; the delivery stays counted",
			append(attrs, "error", err)...)
		return true
	}
	log.Info("loop mailbox delivery failed; delivery refunded", attrs...)
	return false
}

// refundAfterFailedAck gives back a counted delivery whose turn finished
// cleanly but whose ack did not land. The turn did not fail, so the
// items must not be charged for it; acked rows are already gone and the
// refund's receipt guard skips them.
func (l *Loop) refundAfterFailedAck(ctx context.Context, log *slog.Logger, delivery mailboxDelivery) {
	if !delivery.counted || len(delivery.items) == 0 {
		return
	}
	l.mu.Lock()
	mb, name := l.deps.Mailbox, l.config.Name
	l.mu.Unlock()
	wctx, cancel := mailboxSettleContext(ctx)
	defer cancel()
	if err := mb.refundDelivery(wctx, name, delivery.items); err != nil {
		log.Warn("loop mailbox delivery refund after a failed ack failed; the delivery stays counted",
			"item_ids", mailboxItemIDs(delivery.items),
			"error", err,
		)
		return
	}
	log.Info("loop mailbox delivery refunded after a failed ack",
		"item_ids", mailboxItemIDs(delivery.items),
	)
}

// mailboxSettleReason names why a failed turn's delivery is kept or
// given back. Only "builder_failed" keeps it. A loop that is stopping
// comes first, because a turn builder interrupted by the stop fails for
// that reason and not the item's.
func mailboxSettleReason(ctxErr error, handler, builderFailed bool) string {
	switch {
	case ctxErr != nil:
		return "loop_stopping"
	case handler:
		return "handler_failed"
	case !builderFailed:
		return "failed_after_turn_built"
	}
	return "builder_failed"
}

// mailboxSettleContext detaches a post-turn mailbox write from the
// loop's cancellation so a stopping loop still settles the turn it ran.
func mailboxSettleContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), mailboxSettleTimeout)
}

func mailboxItemIDs(items []MailboxItem) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

func queueItems(items []MailboxItem) []loopqueue.Item {
	out := make([]loopqueue.Item, len(items))
	for i, it := range items {
		out[i] = loopqueue.Item{DedupKey: it.ID, Receipt: it.Receipt}
	}
	return out
}

func (m *Mailbox) recordDelivery(ctx context.Context, loopName string, items []MailboxItem) error {
	if m == nil || m.store == nil {
		return nil
	}
	return m.store.RecordDelivery(ctx, loopName, queueItems(items))
}

func (m *Mailbox) refundDelivery(ctx context.Context, loopName string, items []MailboxItem) error {
	if m == nil || m.store == nil {
		return nil
	}
	return m.store.RefundDelivery(ctx, loopName, queueItems(items))
}

func (m *Mailbox) putAside(ctx context.Context, loopName string, items []MailboxItem) (int, error) {
	if m == nil || m.store == nil {
		return 0, nil
	}
	return m.store.PutAside(ctx, loopName, queueItems(items))
}
