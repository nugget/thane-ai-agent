package loop

import (
	"context"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

// Mailbox is a durable data-plane inbox for loop-owned conversation
// content. Control-plane notifications still use pendingNotifies.
type Mailbox struct {
	store *loopqueue.Store
}

// NewMailbox wraps store as a loop mailbox.
func NewMailbox(store *loopqueue.Store) *Mailbox {
	if store == nil {
		return nil
	}
	return &Mailbox{store: store}
}

// MailboxItem is one data-plane input delivered into a loop turn.
type MailboxItem struct {
	ID         string
	Payload    []byte
	EnqueuedAt time.Time
}

// MailboxReceipt summarizes the effect of enqueuing a mailbox item.
type MailboxReceipt struct {
	LoopID            string `json:"loop_id"`
	LoopName          string `json:"loop_name"`
	State             State  `json:"state"`
	ItemID            string `json:"item_id"`
	PendingItems      int    `json:"pending_items,omitempty"`
	WokeImmediately   bool   `json:"woke_immediately,omitempty"`
	QueuedForNextWake bool   `json:"queued_for_next_wake,omitempty"`
}

type mailboxItemsContextKey struct{}

// MailboxItemsFromContext returns mailbox items delivered to the
// current loop iteration, if any.
func MailboxItemsFromContext(ctx context.Context) []MailboxItem {
	items, _ := ctx.Value(mailboxItemsContextKey{}).([]MailboxItem)
	if len(items) == 0 {
		return nil
	}
	out := make([]MailboxItem, len(items))
	copy(out, items)
	return out
}

func withMailboxItems(ctx context.Context, items []MailboxItem) context.Context {
	if len(items) == 0 {
		return ctx
	}
	cp := make([]MailboxItem, len(items))
	copy(cp, items)
	return context.WithValue(ctx, mailboxItemsContextKey{}, cp)
}

func (m *Mailbox) enqueue(ctx context.Context, loopName, keyPrefix string, payload []byte) (MailboxItem, error) {
	if m == nil || m.store == nil {
		return MailboxItem{}, fmt.Errorf("loop mailbox is not configured")
	}
	key, err := m.store.Append(ctx, loopName, keyPrefix, 0, payload)
	if err != nil {
		return MailboxItem{}, err
	}
	return MailboxItem{ID: key, Payload: append([]byte(nil), payload...), EnqueuedAt: time.Now().UTC()}, nil
}

func (m *Mailbox) drain(ctx context.Context, loopName string, limit int) ([]MailboxItem, error) {
	if m == nil || m.store == nil {
		return nil, nil
	}
	var (
		stored []loopqueue.Item
		err    error
	)
	if limit > 0 {
		stored, err = m.store.Peek(ctx, loopName, limit)
	} else {
		stored, err = m.store.PeekAll(ctx, loopName)
	}
	if err != nil {
		return nil, err
	}
	if len(stored) == 0 {
		return nil, nil
	}
	items := make([]MailboxItem, 0, len(stored))
	for _, it := range stored {
		if err := m.store.Ack(ctx, loopName, it.DedupKey); err != nil {
			return nil, err
		}
		items = append(items, MailboxItem{
			ID:         it.DedupKey,
			Payload:    append([]byte(nil), it.Payload...),
			EnqueuedAt: it.EnqueuedAt,
		})
	}
	return items, nil
}

func (m *Mailbox) depth(ctx context.Context, loopName string) (int, error) {
	if m == nil || m.store == nil {
		return 0, nil
	}
	return m.store.PendingCount(ctx, loopName)
}

// EnqueueMailbox durably enqueues payload for this loop and wakes the
// loop if it is running. keyPrefix is used only for readable queue IDs.
func (l *Loop) EnqueueMailbox(ctx context.Context, keyPrefix string, payload []byte) (MailboxReceipt, error) {
	return l.enqueueMailbox(ctx, keyPrefix, payload)
}

func (l *Loop) enqueueMailbox(ctx context.Context, keyPrefix string, payload []byte) (MailboxReceipt, error) {
	if err := ctx.Err(); err != nil {
		return MailboxReceipt{}, err
	}

	l.mu.Lock()
	mailbox := l.deps.Mailbox
	loopID := l.id
	loopName := l.config.Name
	started := l.started
	stopped := l.stopped
	l.mu.Unlock()

	if mailbox == nil {
		return MailboxReceipt{}, fmt.Errorf("loop %q mailbox is not configured", loopName)
	}
	if stopped || !started {
		return MailboxReceipt{}, fmt.Errorf("loop %q is not running", loopName)
	}

	item, err := mailbox.enqueue(ctx, loopName, keyPrefix, payload)
	if err != nil {
		return MailboxReceipt{}, err
	}
	pending, pendingErr := mailbox.depth(ctx, loopName)
	if pendingErr != nil {
		return MailboxReceipt{}, pendingErr
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	receipt := MailboxReceipt{
		LoopID:       loopID,
		LoopName:     loopName,
		State:        l.state,
		ItemID:       item.ID,
		PendingItems: pending,
	}
	select {
	case l.wakeCh <- struct{}{}:
	default:
	}
	if l.state == StateSleeping || l.state == StateWaiting {
		receipt.WokeImmediately = true
	} else {
		receipt.QueuedForNextWake = true
	}
	return receipt, nil
}

// DrainMailbox drains mailbox items for this loop. limit <= 0 drains
// all currently pending items; positive limits provide the incremental
// drain API used by future mid-turn input paths.
func (l *Loop) DrainMailbox(ctx context.Context, limit int) ([]MailboxItem, error) {
	l.mu.Lock()
	mailbox := l.deps.Mailbox
	loopName := l.config.Name
	l.mu.Unlock()
	return mailbox.drain(ctx, loopName, limit)
}

// MailboxDepth returns the number of pending mailbox items for this loop.
func (l *Loop) MailboxDepth(ctx context.Context) (int, error) {
	l.mu.Lock()
	mailbox := l.deps.Mailbox
	loopName := l.config.Name
	l.mu.Unlock()
	return mailbox.depth(ctx, loopName)
}

func (l *Loop) wakeIfMailboxNonEmpty(ctx context.Context) {
	depth, err := l.MailboxDepth(ctx)
	if err != nil {
		l.deps.Logger.Warn("loop mailbox depth check failed",
			"loop_id", l.id,
			"loop_name", l.config.Name,
			"error", err,
		)
		return
	}
	if depth == 0 {
		return
	}
	select {
	case l.wakeCh <- struct{}{}:
	default:
	}
}
