package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

// maxPendingNotifications bounds how many one-shot inter-loop notifications a
// live loop may queue while it is busy or sleeping. enqueueNotify rejects new
// notifications once this cap is reached so a runaway caller cannot grow the
// in-memory pending-notify slice without bound before the loop gets a chance
// to drain it on the next iteration.
const maxPendingNotifications = 8

type pendingNotify struct {
	Envelope        messages.Envelope
	ForceSupervisor bool
	Tags            []string
}

// NotifyReceipt summarizes the effect of notifying a live loop.
type NotifyReceipt struct {
	LoopID               string `json:"loop_id"`
	LoopName             string `json:"loop_name"`
	State                State  `json:"state"`
	WokeImmediately      bool   `json:"woke_immediately,omitempty"`
	QueuedForNextWake    bool   `json:"queued_for_next_wake,omitempty"`
	ForceSupervisor      bool   `json:"force_supervisor,omitempty"`
	PendingNotifications int    `json:"pending_notifications,omitempty"`
}

type notifyContextKey struct{}

type wakeTagsContextKey struct{}

// NotifyEnvelopesFromContext returns one-shot message envelopes delivered to
// the current loop iteration, if any.
func NotifyEnvelopesFromContext(ctx context.Context) []messages.Envelope {
	envs, _ := ctx.Value(notifyContextKey{}).([]messages.Envelope)
	if len(envs) == 0 {
		return nil
	}
	out := make([]messages.Envelope, len(envs))
	copy(out, envs)
	return out
}

func withNotifyEnvelopes(ctx context.Context, envs []messages.Envelope) context.Context {
	if len(envs) == 0 {
		return ctx
	}
	cp := make([]messages.Envelope, len(envs))
	copy(cp, envs)
	return context.WithValue(ctx, notifyContextKey{}, cp)
}

func withWakeTags(ctx context.Context, tags []string) context.Context {
	if len(tags) == 0 {
		return ctx
	}
	cp := make([]string, len(tags))
	copy(cp, tags)
	return context.WithValue(ctx, wakeTagsContextKey{}, cp)
}

func decodeLoopNotifyPayload(raw any) (messages.LoopNotifyPayload, error) {
	switch got := raw.(type) {
	case nil:
		return messages.LoopNotifyPayload{}, nil
	case messages.LoopNotifyPayload:
		return got, nil
	case *messages.LoopNotifyPayload:
		if got == nil {
			return messages.LoopNotifyPayload{}, nil
		}
		return *got, nil
	case map[string]any:
		var payload messages.LoopNotifyPayload
		// Generic decoded JSON payloads arrive as map[string]any.
		blob, err := json.Marshal(got)
		if err != nil {
			return messages.LoopNotifyPayload{}, fmt.Errorf("marshal loop notify payload: %w", err)
		}
		if err := json.Unmarshal(blob, &payload); err != nil {
			return messages.LoopNotifyPayload{}, fmt.Errorf("decode loop notify payload: %w", err)
		}
		return payload, nil
	default:
		return messages.LoopNotifyPayload{}, fmt.Errorf("unsupported loop notify payload %T", raw)
	}
}

type notifyWakeEvent struct{}

type waitResult struct {
	event any
	err   error
}

func (l *Loop) enqueueNotify(env messages.Envelope) (NotifyReceipt, error) {
	payload, err := decodeLoopNotifyPayload(env.Payload)
	if err != nil {
		return NotifyReceipt{}, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped || !l.started {
		return NotifyReceipt{}, fmt.Errorf("loop %q is not running", l.config.Name)
	}
	if len(l.pendingNotifies) >= maxPendingNotifications {
		return NotifyReceipt{}, fmt.Errorf("loop %q notify queue full (%d pending)", l.config.Name, len(l.pendingNotifies))
	}

	tags := cleanNotifyTags(payload.Tags)
	l.pendingNotifies = append(l.pendingNotifies, pendingNotify{
		Envelope:        env,
		ForceSupervisor: payload.ForceSupervisor,
		Tags:            tags,
	})
	receipt := NotifyReceipt{
		LoopID:               l.id,
		LoopName:             l.config.Name,
		State:                l.state,
		ForceSupervisor:      payload.ForceSupervisor,
		PendingNotifications: len(l.pendingNotifies),
	}
	// Signal wakeCh unconditionally. A notification arriving while the
	// loop is in StateProcessing must still poke the channel so the
	// next waitForWake (event-driven loops with no periodic timer) or
	// next sleep (timer-driven loops, which become 0-duration on
	// signal) sees it and drains pendingNotifies. Without this, an
	// event-driven loop that is busy when a notification arrives can
	// strand the message until some later unrelated wake repokes the
	// channel. Spurious wakes are absorbed harmlessly:
	// consumePendingNotifies drains wakeCh when no items are queued.
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

// cleanNotifyTags returns a deduplicated, whitespace-trimmed copy of
// the tag slice, dropping empties. The caller hands ownership of the
// returned slice to pendingNotify; mutation of the source after the
// call does not affect the stored tags.
func cleanNotifyTags(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, tag := range in {
		t := strings.TrimSpace(tag)
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// consumePendingNotifies drains the loop's pending notification
// queue and returns the envelopes alongside the aggregated
// per-iteration directives derived from them:
//
//   - forceSupervisor — true when ANY envelope carried
//     [LoopNotifyPayload.ForceSupervisor]. The supervisor-turn
//     decision OR's across all draining notifications.
//   - tags — the deduplicated union of
//     [LoopNotifyPayload.Tags] across all envelopes. These are
//     iteration-scoped capability tags that the trigger source
//     (forge, MQTT, contacts classifier in email, etc.) wants
//     activated for the upcoming iteration's tool surface and
//     context providers. Empty slice when no envelopes carry tags.
//
// Tags are returned as a separate value (not embedded in the
// envelope return) so callers can merge them into
// [Request.InitialTags] before [prepareAgentTurnRequest] runs the
// final tag aggregation.
func (l *Loop) consumePendingNotifies() ([]messages.Envelope, bool, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.pendingNotifies) == 0 {
		// A concurrent wake can leave one coalesced token behind even after the
		// corresponding notification was already consumed elsewhere; clear it so the
		// next timer sleep is not interrupted spuriously.
		select {
		case <-l.wakeCh:
		default:
		}
		return nil, false, nil
	}
	envs := make([]messages.Envelope, 0, len(l.pendingNotifies))
	forceSupervisor := false
	seenTags := make(map[string]struct{})
	var tags []string
	for _, sig := range l.pendingNotifies {
		envs = append(envs, sig.Envelope)
		forceSupervisor = forceSupervisor || sig.ForceSupervisor
		// Tags are pre-decoded at enqueue time, so all valid payload
		// shapes (LoopNotifyPayload value, *LoopNotifyPayload pointer,
		// map[string]any from a JSON-decoded bus envelope) contribute
		// uniformly. The previous Envelope.Payload type-assert would
		// silently drop tags from the pointer and map forms.
		for _, tag := range sig.Tags {
			if _, dup := seenTags[tag]; dup {
				continue
			}
			seenTags[tag] = struct{}{}
			tags = append(tags, tag)
		}
	}
	l.pendingNotifies = nil
	select {
	case <-l.wakeCh:
	default:
	}
	return envs, forceSupervisor, tags
}

func (l *Loop) waitForEvent(ctx context.Context) (any, error) {
	if l.config.WaitFunc == nil {
		return nil, nil
	}

	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan waitResult, 1)
	go func() {
		event, err := l.config.WaitFunc(waitCtx)
		done <- waitResult{event: event, err: err}
	}()

	for {
		select {
		case result := <-done:
			return result.event, result.err
		case <-l.wakeCh:
			cancel()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return notifyWakeEvent{}, nil
		case <-ctx.Done():
			cancel()
			return nil, ctx.Err()
		case <-l.retuneCh:
			// A retune is not an event: promote and keep waiting so
			// conformance never burns an iteration.
			l.promoteRetune()
		}
	}
}

// sleep blocks for d unless woken early. It returns ok=false when the
// context was cancelled (the loop should exit), and interrupted=true
// when a wakeCh poke — not the timer — ended the sleep, which feeds the
// next iteration's wake attribution as the lowest-priority signal that
// the wake was not the loop's own cadence.
func (l *Loop) sleep(ctx context.Context, d time.Duration) (ok, interrupted bool) {
	// Record the scheduled wake instant so loop_status can report when the
	// loop next fires; clear it on wake so a processing loop never reports a
	// stale deadline. A wakeCh notification can cut the sleep short — this is
	// the *scheduled* deadline, not a guarantee.
	start := time.Now()
	deadline := start.Add(d)
	l.mu.Lock()
	l.sleepUntil = deadline
	l.currentSleep = d
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		// Capture what the sleep actually came to before clearing the
		// in-flight fields. A notification wake makes the elapsed time the
		// honest answer to "how long was I out", and the planned duration
		// read off l.currentSleep (not the d argument, which a mid-sleep
		// retune supersedes) is what it was meant to be.
		l.lastSleptFor = time.Since(start)
		l.lastSleptPlanned = l.currentSleep
		l.sleepUntil = time.Time{}
		l.currentSleep = 0
		l.mu.Unlock()
	}()

	for {
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, false
		case <-timer.C:
			return true, false
		case <-l.wakeCh:
			timer.Stop()
			return true, true
		case <-l.retuneCh:
			timer.Stop()
			// Promote the pending retune and re-clamp this sleep as if
			// the new envelope had governed it from the start: the
			// planned duration is re-clamped (not re-jittered) into the
			// edited envelope, and an overdue deadline wakes the loop
			// now. A stale signal (retune already promoted at a run-loop
			// promote point) re-clamps against an unchanged envelope — a
			// no-op. An unset envelope (SleepMax zero: event-driven
			// wait-error backoff) is left alone rather than clamped to
			// zero, which would burn an iteration.
			l.mu.Lock()
			promoted := l.promoteRetuneLocked()
			nd := d
			if min := l.config.SleepMin; nd < min {
				nd = min
			}
			if max := l.config.SleepMax; max > 0 && nd > max {
				nd = max
			}
			deadline = start.Add(nd)
			l.sleepUntil = deadline
			l.currentSleep = nd
			overdue := !time.Now().Before(deadline)
			if overdue {
				// The edited envelope made this sleep overdue: the loop
				// wakes now, and the retune is what woke it.
				l.recordWakeCauseLocked(wakeCause{reason: WakeReasonRetune})
			}
			l.mu.Unlock()
			if promoted {
				l.deps.Logger.Info("loop retune applied",
					"loop_id", l.id,
					"loop_name", l.config.Name,
					"resleep", nd.Round(time.Second).String(),
				)
			}
			if overdue {
				return true, true
			}
		}
	}
}

// waitForWake blocks until a notification arrives or the context is
// cancelled. The notification path used by event-driven loops
// (operation=event_driven without a [Config.WaitFunc]) — these loops
// have no periodic timer to wake on, so they wait indefinitely on
// the wakeCh that [Loop.enqueueNotify] writes into (reached via the
// [Registry.NotifyLoop] delivery path).
//
// Returns true on wake (a notification arrived; the caller should
// continue to the iteration phase, where consumePendingNotifies
// drains the envelopes), false on context cancellation (the loop
// should exit).
func (l *Loop) waitForWake(ctx context.Context) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-l.wakeCh:
			return true
		case <-l.retuneCh:
			// A retune is not a wake: promote and keep waiting so
			// conformance never burns an iteration.
			l.promoteRetune()
		}
	}
}
