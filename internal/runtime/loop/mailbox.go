package loop

import (
	"context"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
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

const maxMailboxItemsPerWake = 16

// defaultMidTurnInputBudget caps how many times a single turn may pull
// newly-arrived mailbox input mid-flight (#1221). Bounding pulls — not items
// — keeps turn boundaries reachable under continuous inbound flow (#1152):
// once spent, the remainder rides the buffered-wakeCh immediate re-wake.
// Override per loop via Config.MidTurnInputBudget.
const defaultMidTurnInputBudget = 8

func (l *Loop) midTurnInputBudget() int {
	if l.config.MidTurnInputBudget > 0 {
		return l.config.MidTurnInputBudget
	}
	return defaultMidTurnInputBudget
}

// buildMailboxPullInput returns a PullInput closure over this loop's mailbox
// for the #1221 mid-turn input merge. Each call peeks pending items, skips any
// already delivered this turn — the wake batch plus prior pulls (delta-gating,
// so content is never re-presented) — caps the number of pulls per turn
// (drain budget), renders the fresh items via render (channel voice), appends
// them to *pulled so the turn-end ack covers them, and returns the rendered
// messages. Returns nil once the budget is spent or nothing new is pending,
// which the engine reads as "no mid-turn input."
func (l *Loop) buildMailboxPullInput(
	wakeBatch []MailboxItem,
	pulled *[]MailboxItem,
	render func(context.Context, []MailboxItem) []llm.Message,
) func(context.Context) []llm.Message {
	delivered := make(map[string]bool, len(wakeBatch))
	for _, it := range wakeBatch {
		delivered[it.ID] = true
	}
	budget := l.midTurnInputBudget()
	pulls := 0

	return func(ctx context.Context) []llm.Message {
		if pulls >= budget {
			return nil
		}
		items, err := l.DrainMailbox(ctx, 0) // peek all pending; delta-gate below
		if err != nil {
			l.deps.Logger.Warn("mid-turn mailbox peek failed",
				"loop_id", l.id, "loop_name", l.config.Name, "error", err)
			return nil
		}
		var fresh []MailboxItem
		for _, it := range items {
			if !delivered[it.ID] {
				fresh = append(fresh, it)
			}
		}
		if len(fresh) == 0 {
			return nil
		}
		rendered := render(ctx, fresh)
		if len(rendered) == 0 {
			return nil
		}
		pulls++
		for _, it := range fresh {
			delivered[it.ID] = true
		}
		*pulled = append(*pulled, fresh...)
		return rendered
	}
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
	return m.Enqueue(ctx, loopName, keyPrefix, payload)
}

// Enqueue durably appends one payload to a loop-name mailbox partition.
// The loop name is the durable consumer identity; mailbox-enabled loops
// must keep names unique while they share a store.
func (m *Mailbox) Enqueue(ctx context.Context, loopName, keyPrefix string, payload []byte) (MailboxItem, error) {
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
	return m.Peek(ctx, loopName, limit)
}

// Peek returns pending mailbox items for loopName without acknowledging
// them. Callers must ack only after the items have been handled.
func (m *Mailbox) Peek(ctx context.Context, loopName string, limit int) ([]MailboxItem, error) {
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
		items = append(items, MailboxItem{
			ID:         it.DedupKey,
			Payload:    append([]byte(nil), it.Payload...),
			EnqueuedAt: it.EnqueuedAt,
		})
	}
	return items, nil
}

func (m *Mailbox) ack(ctx context.Context, loopName string, items []MailboxItem) error {
	if m == nil || m.store == nil || len(items) == 0 {
		return nil
	}
	for _, item := range items {
		if err := m.store.Ack(ctx, loopName, item.ID); err != nil {
			return err
		}
	}
	return nil
}

func (m *Mailbox) depth(ctx context.Context, loopName string) (int, error) {
	if m == nil || m.store == nil {
		return 0, nil
	}
	return m.store.PendingCount(ctx, loopName)
}

// PendingConsumers returns loop-name partitions with pending mailbox
// work, optionally restricted to names that share prefix.
func (m *Mailbox) PendingConsumers(ctx context.Context, prefix string) ([]string, error) {
	if m == nil || m.store == nil {
		return nil, nil
	}
	return m.store.PendingConsumers(ctx, prefix)
}

// MoveConsumer reassigns pending mailbox rows from one durable loop-name
// partition to another.
func (m *Mailbox) MoveConsumer(ctx context.Context, from, to string) error {
	if m == nil || m.store == nil {
		return nil
	}
	return m.store.MoveConsumer(ctx, from, to)
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

// DrainMailbox peeks mailbox items for this loop. The rows remain
// pending until [Loop.AckMailbox] is called after successful handling.
// limit <= 0 drains all currently pending items; positive limits provide
// incremental delivery for capped wake batches.
func (l *Loop) DrainMailbox(ctx context.Context, limit int) ([]MailboxItem, error) {
	l.mu.Lock()
	mailbox := l.deps.Mailbox
	loopName := l.config.Name
	l.mu.Unlock()
	return mailbox.drain(ctx, loopName, limit)
}

// AckMailbox removes mailbox items that were successfully handled by
// this loop.
func (l *Loop) AckMailbox(ctx context.Context, items []MailboxItem) error {
	l.mu.Lock()
	mailbox := l.deps.Mailbox
	loopName := l.config.Name
	l.mu.Unlock()
	return mailbox.ack(ctx, loopName, items)
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
	l.pokeWake()
}

func (l *Loop) pokeWake() {
	select {
	case l.wakeCh <- struct{}{}:
	default:
	}
}
