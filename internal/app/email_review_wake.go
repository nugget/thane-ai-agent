package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
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
// for work queued before a restart, and a recheck review_delay after
// every wake for work that wake left behind (email_review_recheck.go),
// whether or not mail is polled. A wake is sent only while the partition
// holds work, so an idle queue never wakes the loop.

// emailReviewWakeSource names the producer on review wakes.
const emailReviewWakeSource = "email_review"

// The wake counts what waits by each queued item's subject, which the
// email service keys by kind: draft:<account>:<draft_id> and
// message:<account>:<message_id>.
const (
	// emailReviewSubjectDraft starts the subject of a queued draft.
	emailReviewSubjectDraft = "draft:"
	// emailReviewSubjectMessage starts the subject of an escalated message.
	emailReviewSubjectMessage = "message:"
)

// Why a wake attempt runs, as its log lines name it.
const (
	// emailReviewReasonEnqueue is the debounced fire after an enqueue.
	emailReviewReasonEnqueue = "enqueue"
	// emailReviewReasonBootSweep is the sweep once the loops have started.
	emailReviewReasonBootSweep = "boot_sweep"
	// emailReviewReasonLeftover is the recheck after a wake.
	emailReviewReasonLeftover = "leftover_work"
	// emailReviewReasonRetry is the recheck after an attempt that could
	// not read the queue.
	emailReviewReasonRetry = "retry"
)

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

// emailReviewDue decides whether a wake is due for a review loop, given
// its pending work split at the mark of its last wake. Caller holds
// w.mu.
type emailReviewDue func(name string, split loopqueue.PendingSplit) bool

// emailReviewWaker wakes each review loop while its queue holds work.
//
// Three paths wake a loop: the debounced fire after an enqueue, the boot
// sweep, and the recheck after a wake. mu serializes them, so two never
// announce the same work at once, and every wake records the queue mark
// it announced up to. A coalesced enqueue gives its item a receipt after
// that mark, so it is new work again; a deferral keeps the receipt, so a
// deferred item stays announced. Every read is an aggregate over the
// partition, so however large a backlog grows, a wake loads none of it.
type emailReviewWaker struct {
	queue  *loopqueue.Store
	bus    *messages.Bus
	logger *slog.Logger
	loops  map[string]emailReviewTiming
	now    func() time.Time

	// wakeTimeout bounds each wake attempt from its start, the wait for
	// mu included, the way queueWakeDeliveryTimeout bounds each hop of
	// the queued wake dispatcher, so a queue read or a delivery that
	// does not return holds neither the attempt nor mu past it.
	wakeTimeout time.Duration
	// recheckFloor is the shortest time from a wake to its recheck.
	recheckFloor time.Duration

	mu       sync.Mutex
	lastWake map[string]time.Time
	marks    map[string]string // loop -> the queue mark its last wake announced up to

	// life is cancelled by stop, and every worker's attempt runs under
	// it. lifeMu guards stopped and rechecks, and orders launches
	// before stop's wait for the workers (email_review_recheck.go).
	life       context.Context
	stopLife   context.CancelFunc
	lifeMu     sync.Mutex
	stopped    bool
	rechecks   map[string]*emailReviewRecheck
	recheckGen uint64
	workers    sync.WaitGroup
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
	life, stopLife := context.WithCancel(context.Background())
	return &emailReviewWaker{
		queue:        queue,
		bus:          bus,
		logger:       logger,
		loops:        loops,
		now:          time.Now,
		wakeTimeout:  queueWakeDeliveryTimeout,
		recheckFloor: emailReviewRecheckFloor,
		lastWake:     make(map[string]time.Time),
		marks:        make(map[string]string),
		life:         life,
		stopLife:     stopLife,
		rechecks:     make(map[string]*emailReviewRecheck),
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
// wake to a worker (launch). A fire wakes the loop only for work no wake
// has announced yet: a boot sweep or a recheck that ran while the
// debounce was pending already told the loop about it.
func (w *emailReviewWaker) arm() {
	if w == nil {
		return
	}
	for _, name := range w.names() {
		timing := w.loops[name]
		w.queue.SetWakeOnEnqueue(name, timing.delay, timing.maxWait, func() {
			w.launch(name, emailReviewReasonEnqueue, w.hasNewWork)
		})
		w.logger.Info("email review loop armed to wake on queued work",
			"review_loop", name, "review_delay", timing.delay, "review_max_wait", timing.maxWait)
	}
}

// Sweep wakes every review loop whose queue holds work no wake has
// announced, which at boot is all of it. It runs once, after the loops
// have started, because a debounce pending when the process stopped did
// not survive it; a debounce that fired between the loops starting and
// the sweep has already announced its work, so the sweep does not
// announce it again.
func (w *emailReviewWaker) Sweep(ctx context.Context) {
	if w == nil {
		return
	}
	for _, name := range w.names() {
		w.wakeFor(ctx, name, emailReviewReasonBootSweep, w.hasNewWork)
	}
}

// hasNewWork reports whether some pending item was enqueued, or
// enqueued again, after the loop's last wake announced its queue.
// Caller holds w.mu.
func (w *emailReviewWaker) hasNewWork(_ string, split loopqueue.PendingSplit) bool {
	return split.Since > 0
}

// leftoverDue reports whether the recheck interval has passed since the
// loop's last wake and some item that wake announced still waits
// unchanged: a batch larger than one queue_pull, an item the loop
// deferred, or a wake the bus did not deliver. Work queued after that
// wake is not leftover: it waits out its own debounce, bounded by
// review_max_wait, so a steady stream of new work is never woken early.
// Caller holds w.mu.
func (w *emailReviewWaker) leftoverDue(name string, split loopqueue.PendingSplit) bool {
	last, woke := w.lastWake[name]
	return woke && split.Through > 0 && w.now().Sub(last) >= w.recheckInterval(name)
}

// wakeFor sends one wake to a review loop whose queue holds work that
// due approves, naming how many drafts and messages wait, and sends
// nothing for an empty queue. wakeTimeout bounds the
// whole attempt, starting before it waits for mu. A wake records what
// it announced and arms the loop's recheck before it is sent, so a wake
// the bus cannot deliver is retried at that recheck; an attempt that
// cannot read the queue in time is logged and retried at the recheck
// floor (wakeNotRead).
func (w *emailReviewWaker) wakeFor(ctx context.Context, name, reason string, due emailReviewDue) {
	ctx, cancel := context.WithTimeout(ctx, w.wakeTimeout)
	defer cancel()
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		w.wakeNotRead(name, reason, due, err)
		return
	}
	split, err := w.queue.SplitPending(ctx, name, w.marks[name])
	if err != nil {
		w.wakeNotRead(name, reason, due, err)
		return
	}
	if split.Pending() == 0 {
		w.disarmRecheck(name)
		w.logger.Debug("email review queue empty; loop not woken", "review_loop", name, "reason", reason)
		return
	}
	if !due(name, split) {
		w.keepLeftoverRecheck(name, split)
		w.logger.Debug("email review wake not due; loop not woken", "review_loop", name, "reason", reason,
			"announced", split.Through, "since_last_wake", split.Since)
		return
	}
	kinds, pending, err := w.queue.PendingCountsByKeyPrefix(ctx, name, []string{emailReviewSubjectDraft, emailReviewSubjectMessage})
	if err != nil {
		w.wakeNotRead(name, reason, due, err)
		return
	}
	drafts, msgs := kinds[emailReviewSubjectDraft], kinds[emailReviewSubjectMessage]
	now := w.now()
	w.lastWake[name] = now
	w.marks[name] = split.Mark
	w.armRecheck(name, emailReviewReasonLeftover, w.recheckInterval(name), w.leftoverDue)

	event := messages.LoopEventPayload{
		Source:     emailReviewWakeSource,
		Type:       "review_pending",
		ID:         fmt.Sprintf("email-review-%s-%d", name, now.UnixMilli()),
		Title:      "Email review queue",
		Summary:    emailReviewWakeSummary(drafts, msgs, pending),
		ObservedAt: now,
		Metadata: map[string]string{
			"drafts":   strconv.Itoa(drafts),
			"messages": strconv.Itoa(msgs),
			"pending":  strconv.Itoa(pending),
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
	if _, err := w.bus.Send(ctx, env); err != nil {
		w.logger.Warn("email review wake not delivered; retrying at the recheck", "review_loop", name, "reason", reason,
			"drafts", drafts, "messages", msgs, "retry_in", w.recheckInterval(name), "error", err)
		return
	}
	w.logger.Info("email review loop woken", "review_loop", name, "reason", reason, "drafts", drafts, "messages", msgs, "pending", pending)
}

// wakeNotRead logs an attempt that could not read name's queue, in time
// or at all, and retries it once the recheck floor has passed, not a
// whole review_delay later, so a stalled store holds a due wake back by
// about the floor. The retry is still for the work the attempt was after
// and for any announced work left since. An attempt cancelled by
// shutdown is not retried. Caller holds w.mu.
func (w *emailReviewWaker) wakeNotRead(name, reason string, due emailReviewDue, err error) {
	if errors.Is(err, context.Canceled) {
		w.logger.Debug("email review wake cancelled", "review_loop", name, "reason", reason, "error", err)
		return
	}
	w.logger.Warn("email review queue not read for wake; retrying", "review_loop", name, "reason", reason,
		"retry_in", w.recheckFloor, "error", err)
	w.armRecheck(name, emailReviewReasonRetry, w.recheckFloor, eitherDue(due, w.leftoverDue))
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
