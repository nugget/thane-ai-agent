package app

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

// An email review loop is a loopqueue consumer, like the archivist, and
// its partition is its own loop name, which is the partition its
// queue_pull drains. The email service enqueues into it (a draft an
// unattended turn wrote, a message email_escalate handed over), and this
// file turns that durable work into wakes: a debounced wake on enqueue
// shaped by the account's review_delay and review_max_wait, a boot sweep
// for work queued before a restart, and a re-wake from the email
// poller for work a wake left behind. A wake is sent only while the
// partition holds work, so an idle queue never wakes the loop.

// emailReviewWakeSource names the producer on review wakes.
const emailReviewWakeSource = "email_review"

// emailReviewQueueToolNames are the queue tools a review loop carries:
// it drains, acknowledges, and defers its own queue, and never enqueues
// into it, because its work comes only from drafts and escalations.
var emailReviewQueueToolNames = []string{"queue_pull", "queue_ack", "queue_defer"}

// emailReviewTiming is one review loop's wake shape.
type emailReviewTiming struct {
	delay   time.Duration
	maxWait time.Duration
}

// emailReviewLoops maps every review loop an account names to its wake
// shape. Accounts sharing a loop share its one registration, and the
// shortest delay and the shortest max wait among them govern, so no
// account's work waits longer than its own configuration allows.
func emailReviewLoops(accounts []config.EmailAccountConfig) map[string]emailReviewTiming {
	loops := make(map[string]emailReviewTiming)
	for _, acct := range accounts {
		name := acct.ReviewLoopName()
		if name == "" {
			continue
		}
		timing := emailReviewTiming{delay: acct.ReviewDelay(), maxWait: acct.ReviewMaxWait()}
		if cur, ok := loops[name]; ok {
			timing.delay = min(timing.delay, cur.delay)
			timing.maxWait = min(timing.maxWait, cur.maxWait)
		}
		loops[name] = timing
	}
	return loops
}

// emailReviewWaker wakes each review loop while its queue holds work.
//
// Three paths wake a loop: the debounced fire after an enqueue, the boot
// sweep, and the re-wake after a poll. mu serializes them, so two never
// announce the same work at once, and every wake records what it
// announced: each pending item's subject and receipt. A coalesced
// enqueue gives its item a new receipt, so it is new work again; a
// deferral keeps the receipt, so a deferred item stays announced.
type emailReviewWaker struct {
	queue  *loopqueue.Store
	bus    *messages.Bus
	logger *slog.Logger
	loops  map[string]emailReviewTiming
	now    func() time.Time

	mu        sync.Mutex
	lastWake  map[string]time.Time
	announced map[string]map[string]string // loop -> subject -> receipt
}

// newEmailReviewWaker returns the waker for the review loops the
// accounts name, or nil when there are none or no queue or bus to wake
// them through.
func newEmailReviewWaker(queue *loopqueue.Store, bus *messages.Bus, accounts []config.EmailAccountConfig, logger *slog.Logger) *emailReviewWaker {
	loops := emailReviewLoops(accounts)
	if queue == nil || bus == nil || len(loops) == 0 {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &emailReviewWaker{
		queue:     queue,
		bus:       bus,
		logger:    logger,
		loops:     loops,
		now:       time.Now,
		lastWake:  make(map[string]time.Time),
		announced: make(map[string]map[string]string),
	}
}

// names returns the review loops in a stable order.
func (w *emailReviewWaker) names() []string {
	names := make([]string, 0, len(w.loops))
	for name := range w.loops {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// arm registers a debounced wake on enqueue for every review loop. The
// fire callback must not block (loopqueue contract), so it hands the
// wake to a goroutine. A fire wakes the loop only for work no wake has
// announced yet: a boot sweep or a re-wake that ran while the debounce
// was pending already told the loop about it.
func (w *emailReviewWaker) arm() {
	if w == nil {
		return
	}
	for _, name := range w.names() {
		timing := w.loops[name]
		w.queue.SetWakeOnEnqueue(name, timing.delay, timing.maxWait, func() {
			go w.wakeFor(context.Background(), name, "enqueue", w.hasNewWork)
		})
		w.logger.Info("email review loop armed to wake on queued work",
			"review_loop", name, "review_delay", timing.delay, "review_max_wait", timing.maxWait)
	}
}

// Sweep wakes every review loop whose queue holds work. It runs once at
// boot, after the loops have started, because a debounce pending when
// the process stopped did not survive it.
func (w *emailReviewWaker) Sweep(ctx context.Context) {
	if w == nil {
		return
	}
	for _, name := range w.names() {
		w.wakeFor(ctx, name, "boot_sweep", nil)
	}
}

// resweep wakes a review loop again for work its last wake announced
// and left in the queue, such as a batch larger than one queue_pull or
// an item it deferred, once review_delay has passed since that wake.
// Work queued after the last wake is not leftover: it waits out its own
// debounce, bounded by review_max_wait, so a steady stream of new work
// is never woken early by a poll. The email poller calls it after every
// poll.
func (w *emailReviewWaker) resweep(ctx context.Context) {
	if w == nil {
		return
	}
	for _, name := range w.names() {
		w.wakeFor(ctx, name, "leftover_work", w.leftoverDue)
	}
}

// hasNewWork reports whether any pending item is one the loop's last
// wake did not announce, or announced under an older receipt. Caller
// holds w.mu.
func (w *emailReviewWaker) hasNewWork(name string, items []loopqueue.Item) bool {
	announced := w.announced[name]
	for _, item := range items {
		if receipt, ok := announced[item.DedupKey]; !ok || receipt != item.Receipt {
			return true
		}
	}
	return false
}

// leftoverDue reports whether review_delay has passed since the loop's
// last wake and some item that wake announced still waits unchanged.
// Caller holds w.mu.
func (w *emailReviewWaker) leftoverDue(name string, items []loopqueue.Item) bool {
	last, woke := w.lastWake[name]
	if !woke || w.now().Sub(last) < w.loops[name].delay {
		return false
	}
	announced := w.announced[name]
	for _, item := range items {
		if receipt, ok := announced[item.DedupKey]; ok && receipt == item.Receipt {
			return true
		}
	}
	return false
}

// wakeFor sends one wake to a review loop whose queue holds work that
// due approves (nil approves any), naming how many drafts and messages
// wait, and sends nothing for an empty queue. The wake records what it
// announced before it is sent, so a wake the bus cannot deliver is
// retried by the next re-wake once review_delay has passed.
func (w *emailReviewWaker) wakeFor(ctx context.Context, name, reason string, due func(string, []loopqueue.Item) bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	items, err := w.queue.PeekAll(ctx, name)
	if err != nil {
		w.logger.Warn("email review queue not read for wake", "review_loop", name, "reason", reason, "error", err)
		return
	}
	if len(items) == 0 {
		w.logger.Debug("email review queue empty; loop not woken", "review_loop", name, "reason", reason)
		return
	}
	if due != nil && !due(name, items) {
		w.logger.Debug("email review work already announced; loop not woken", "review_loop", name, "reason", reason, "pending", len(items))
		return
	}
	drafts, msgs := 0, 0
	announced := make(map[string]string, len(items))
	for _, item := range items {
		announced[item.DedupKey] = item.Receipt
		switch {
		case strings.HasPrefix(item.DedupKey, "draft:"):
			drafts++
		case strings.HasPrefix(item.DedupKey, "message:"):
			msgs++
		}
	}
	now := w.now()
	w.lastWake[name] = now
	w.announced[name] = announced

	event := messages.LoopEventPayload{
		Source:     emailReviewWakeSource,
		Type:       "review_pending",
		ID:         fmt.Sprintf("email-review-%s-%d", name, now.UnixMilli()),
		Title:      "Email review queue",
		Summary:    emailReviewWakeSummary(drafts, msgs, len(items)),
		ObservedAt: now,
		Metadata: map[string]string{
			"drafts":   strconv.Itoa(drafts),
			"messages": strconv.Itoa(msgs),
			"pending":  strconv.Itoa(len(items)),
		},
	}
	env, err := messages.NewEventSourceEnvelope(
		messages.Identity{Kind: messages.IdentitySystem, Name: emailReviewWakeSource},
		messages.LoopWakeTarget{Name: name},
		emailReviewWakeSource,
		[]messages.LoopEventPayload{event},
	)
	if err != nil {
		w.logger.Warn("email review wake not built", "review_loop", name, "reason", reason, "error", err)
		return
	}
	deliveryCtx, cancel := context.WithTimeout(ctx, queueWakeDeliveryTimeout)
	defer cancel()
	if _, err := w.bus.Send(deliveryCtx, env); err != nil {
		w.logger.Warn("email review wake not delivered", "review_loop", name, "reason", reason, "drafts", drafts, "messages", msgs, "error", err)
		return
	}
	w.logger.Info("email review loop woken", "review_loop", name, "reason", reason, "drafts", drafts, "messages", msgs, "pending", len(items))
}

// emailReviewWakeSummary says what waits, in the words the review task
// uses.
func emailReviewWakeSummary(drafts, msgs, pending int) string {
	plural := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.Itoa(n) + " " + many
	}
	return fmt.Sprintf("%s and %s await review (%d queued); call queue_pull.",
		plural(drafts, "draft", "drafts"), plural(msgs, "message", "messages"), pending)
}

// emailReviewQueueTools returns the queue tools a review loop carries,
// scoped to its own partition and worded for a loop its queue wakes.
func emailReviewQueueTools(store *loopqueue.Store, loopName string) []looppkg.RuntimeTool {
	var out []looppkg.RuntimeTool
	for _, tool := range buildQueueTools(store, loopName, queuePaceWake) {
		if slices.Contains(emailReviewQueueToolNames, tool.Name) {
			out = append(out, tool)
		}
	}
	return out
}

// emailReviewLoopNamed reports whether some account names loop as its
// review_loop.
func emailReviewLoopNamed(cfg *config.Config, loop string) bool {
	if cfg == nil {
		return false
	}
	for _, acct := range cfg.Email.Accounts {
		if acct.ReviewLoopName() != "" && acct.ReviewLoopName() == loop {
			return true
		}
	}
	return false
}

// attachEmailReviewTools gives a loop some account names as its
// review_loop the queue tools over its own partition. Like the
// archivist's, they are loop-private runtime tools, never registered
// globally, so nothing else can drain the review queue.
func (a *App) attachEmailReviewTools(spec looppkg.Spec) looppkg.Spec {
	if a == nil || a.loopQueue == nil || !emailReviewLoopNamed(a.cfg, spec.Name) {
		return spec
	}
	spec.RuntimeTools = append(spec.RuntimeTools, emailReviewQueueTools(a.loopQueue, spec.Name)...)
	return spec
}

// startEmailReviewPasses runs once every enabled loop has started. It
// refuses a misrouted account first, because with polling off the
// poller's hydration never runs the check and drafts and escalations
// would queue into a partition nothing drains, and then wakes review
// loops for work queued before a restart.
func (a *App) startEmailReviewPasses(ctx context.Context) error {
	if a == nil || a.cfg == nil || !a.cfg.Email.Configured() {
		return nil
	}
	if err := a.validateEmailRoutes(); err != nil {
		return err
	}
	a.emailReviewWake.Sweep(ctx)
	return nil
}

// validateEmailRoutes refuses an account whose wake_loop or review_loop
// names no event_driven loop definition: a misspelled name would
// otherwise drop that account's wakes, or its review work, on the
// floor, and a loop of another operation would never receive one. It
// runs when the email poller hydrates and again once every loop has
// started (startEmailReviewPasses). wake_loop is checked only while
// mail is polled, because nothing else wakes it; review_loop is always
// checked, because drafts and escalations queue for it either way.
func (a *App) validateEmailRoutes() error {
	if a == nil || a.cfg == nil {
		return nil
	}
	polling := emailServicesEnabled(a.cfg)
	for i, acct := range a.cfg.Email.Accounts {
		if polling {
			unset := fmt.Sprintf("leave wake_loop unset for the built-in %q", acct.DefaultWakeLoopName())
			if err := a.requireEventDrivenDefinition(i, acct, "wake_loop", acct.WakeLoopName(), unset); err != nil {
				return err
			}
		}
		if review := acct.ReviewLoopName(); review != "" {
			if err := a.requireEventDrivenDefinition(i, acct, "review_loop", review, "leave review_loop unset to run the account without a review pass"); err != nil {
				return err
			}
		}
	}
	return nil
}

// requireEventDrivenDefinition refuses a routing key that names no
// event_driven definition.
func (a *App) requireEventDrivenDefinition(i int, acct config.EmailAccountConfig, key, name, unset string) error {
	spec, ok := a.loopDefinitionRegistry.Get(name)
	if !ok {
		return fmt.Errorf("email.accounts[%d] (%s): mailbox.%s %q names no loop definition; define an event_driven loop by that name in a core loops/ document or under loops.definitions, or %s", i, acct.Name, key, name, unset)
	}
	if spec.Operation != looppkg.OperationEventDriven {
		return fmt.Errorf("email.accounts[%d] (%s): mailbox.%s %q names a loop definition whose operation is %q; mail reaches a loop only as a wake, so it must be operation: event_driven, or %s", i, acct.Name, key, name, spec.Operation, unset)
	}
	return nil
}
