package loop

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

// WakeReason classifies why a loop iteration began. Every iteration
// start resolves exactly one reason, chosen by
// [resolveWakeAttribution]; the value flows onto [Status],
// [LoopView], the loop_iteration_start event, and the trailing-24h
// wake ring so cadence questions ("who keeps waking this loop?") are
// answerable without correlating event logs by hand.
type WakeReason string

const (
	// WakeReasonTimer is an ordinary scheduled wake: the sleep
	// deadline expired and nothing else claimed the iteration.
	WakeReasonTimer WakeReason = "timer"

	// WakeReasonStartup is the first iteration after boot, reached
	// through the jittered initial sleep rather than a self-chosen
	// cadence. Distinguished from timer so a restart-heavy day does
	// not read as the loop's own rhythm.
	WakeReasonStartup WakeReason = "startup"

	// WakeReasonNotify is an inter-loop control notification
	// (request_core_attention, poller signals, and other
	// [Registry.NotifyLoop] traffic) that does not classify as
	// manual or subscription. Source carries the sender identity.
	WakeReasonNotify WakeReason = "notify"

	// WakeReasonManual is a directed loop_wake tool call. Source
	// carries the caller identity so "who keeps poking this loop"
	// is a recorded fact.
	WakeReasonManual WakeReason = "manual"

	// WakeReasonSubscription is an event-source wake: a structured
	// event batch delivered by a wake dispatcher (entity
	// subscriptions, MQTT wake topics, and future event sources).
	// Source names the dispatcher, e.g. "subscription_wake" or
	// "mqtt_wake".
	WakeReasonSubscription WakeReason = "subscription"

	// WakeReasonMailbox is fresh durable data-plane input: an item
	// was enqueued to the loop's mailbox and the enqueue woke the
	// loop. Source carries the enqueue key prefix (the producer's
	// readable label) when known, or "external" for a bare
	// [Registry.WakeLoop] nudge toward already-durable work.
	WakeReasonMailbox WakeReason = "mailbox"

	// WakeReasonMailboxRetry is a re-wake for retained rows: a
	// prior turn left mailbox items undelivered (drain cap, failed
	// turn, boot backlog) and the runtime re-armed the wake so they
	// retry.
	WakeReasonMailboxRetry WakeReason = "mailbox_retry"

	// WakeReasonRetune is a mid-sleep spec retune whose edited
	// envelope made the current sleep deadline overdue, waking the
	// loop immediately.
	WakeReasonRetune WakeReason = "retune"

	// WakeReasonEvent is a WaitFunc payload delivery on a legacy
	// channel-reader loop.
	WakeReasonEvent WakeReason = "event"
)

// WakeAttribution is the resolved cause of one iteration start.
// Reason is always set once the first iteration begins; Source is
// the sender/producer identity when the wake carried one and empty
// otherwise.
type WakeAttribution struct {
	Reason WakeReason `json:"reason"`
	Source string     `json:"source,omitempty"`
}

// wakeCause is one recorded content-detached wake poke, appended
// under l.mu at the poke site and drained by the next iteration's
// [Loop.beginIterationWake]. Only pokes whose cause is not
// recoverable from drained content are recorded: mailbox enqueues
// (the drained item does not carry its producer label), retained-row
// re-wakes, external nudges, and retune-overdue returns.
// Notification envelopes are NOT recorded here — they are classified
// from the drained envelopes themselves, which is authoritative for
// exactly the content the iteration processes.
type wakeCause struct {
	reason WakeReason
	source string
}

// maxPendingWakeCauses bounds the recorded pokes between iterations.
// The first cause after a drain is the one that actually woke the
// loop; later ones are pile-on, so overflow drops the newest.
const maxPendingWakeCauses = 8

// recordWakeCauseLocked appends one poke cause. Called with l.mu held.
func (l *Loop) recordWakeCauseLocked(c wakeCause) {
	if len(l.pendingWakeCauses) >= maxPendingWakeCauses {
		return
	}
	l.pendingWakeCauses = append(l.pendingWakeCauses, c)
}

// classifyNotifyWake maps one drained notification envelope to its
// wake attribution. The payload kind is the wire-level discriminator:
// "loop_wake" is the directed wake tool, "event_source" is the shape
// every wake dispatcher builds via [messages.NewEventSourceEnvelope];
// anything else is ordinary inter-loop notification traffic.
func classifyNotifyWake(env messages.Envelope) WakeAttribution {
	payload, _ := decodeLoopNotifyPayload(env.Payload)
	switch payload.Kind {
	case "loop_wake":
		return WakeAttribution{Reason: WakeReasonManual, Source: env.From.Name}
	case "event_source":
		return WakeAttribution{Reason: WakeReasonSubscription, Source: env.From.Name}
	default:
		return WakeAttribution{Reason: WakeReasonNotify, Source: env.From.Name}
	}
}

// Attribution precedence ranks. Content the iteration actually
// drained outranks recorded pokes, and control-plane outranks
// data-plane, so an iteration that processes both a notification and
// mailbox items attributes to the notification while the event data
// still counts both.
const (
	wakeRankManual       = 60
	wakeRankSubscription = 50
	wakeRankNotify       = 40
	wakeRankEvent        = 35
	wakeRankMailbox      = 30
	wakeRankMailboxRetry = 20
	wakeRankRetune       = 15
	wakeRankMailboxNoRec = 10
)

// resolveWakeAttribution decides why an iteration is starting.
//
// Deterministic precedence: drained notification envelopes (manual >
// subscription > notify) beat a WaitFunc event payload, which beats
// mailbox causes (fresh enqueue > retained-row retry, and both
// require drained items so a poke that raced past this iteration's
// drain cannot claim it), which beat a retune-overdue return. With
// nothing to attribute, an interrupted sleep or an event-driven wake
// is an honest "notify/unknown" (a coalesced token whose content was
// consumed elsewhere), the first timer iteration after boot is
// "startup", and everything else is the timer.
func resolveWakeAttribution(causes []wakeCause, signals []messages.Envelope, mailboxItems []MailboxItem, event any, interrupted, firstIteration, eventDriven bool) WakeAttribution {
	best := WakeAttribution{}
	bestRank := 0
	consider := func(a WakeAttribution, rank int) {
		if rank > bestRank {
			best = a
			bestRank = rank
		}
	}

	for _, env := range signals {
		attr := classifyNotifyWake(env)
		switch attr.Reason {
		case WakeReasonManual:
			consider(attr, wakeRankManual)
		case WakeReasonSubscription:
			consider(attr, wakeRankSubscription)
		default:
			consider(attr, wakeRankNotify)
		}
	}

	if event != nil {
		if _, isNotifyPoke := event.(notifyWakeEvent); !isNotifyPoke {
			consider(WakeAttribution{Reason: WakeReasonEvent}, wakeRankEvent)
		}
	}

	if len(mailboxItems) > 0 {
		// Items are present, so a mailbox-family cause is corroborated.
		// Without any recorded cause the items still explain the wake
		// (their poke was consumed by an earlier iteration that hit the
		// drain cap), just without a producer label.
		consider(WakeAttribution{Reason: WakeReasonMailbox}, wakeRankMailboxNoRec)
		for _, c := range causes {
			switch c.reason {
			case WakeReasonMailbox:
				consider(WakeAttribution{Reason: WakeReasonMailbox, Source: c.source}, wakeRankMailbox)
			case WakeReasonMailboxRetry:
				consider(WakeAttribution{Reason: WakeReasonMailboxRetry, Source: c.source}, wakeRankMailboxRetry)
			}
		}
	}

	for _, c := range causes {
		if c.reason == WakeReasonRetune {
			consider(WakeAttribution{Reason: WakeReasonRetune}, wakeRankRetune)
		}
	}

	if bestRank > 0 {
		return best
	}
	if interrupted || eventDriven {
		return WakeAttribution{Reason: WakeReasonNotify, Source: "unknown"}
	}
	if firstIteration {
		return WakeAttribution{Reason: WakeReasonStartup}
	}
	return WakeAttribution{Reason: WakeReasonTimer}
}

// beginIterationWake stamps the attribution for the iteration
// beginning at, drains the recorded poke causes, and records the wake
// in the trailing-24h ring — one critical section, so a Status
// snapshot can never observe a wake without its reason.
func (l *Loop) beginIterationWake(at time.Time, signals []messages.Envelope, mailboxItems []MailboxItem, event any, interrupted, firstIteration bool) WakeAttribution {
	l.mu.Lock()
	defer l.mu.Unlock()
	causes := l.pendingWakeCauses
	l.pendingWakeCauses = nil
	attr := resolveWakeAttribution(causes, signals, mailboxItems, event, interrupted, firstIteration, l.isEventDriven())
	l.lastWakeAttr = attr
	l.recordWakeLocked(at, attr.Reason)
	return attr
}
