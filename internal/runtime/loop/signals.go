package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

// priorityRank maps a message priority to a sort key. Higher rank
// renders first in summarizeNotifyEnvelopes, so urgent notifications
// lead the prompt and the model sees the most important wake content
// before any normal- or low-priority companions.
func priorityRank(p messages.Priority) int {
	switch p {
	case messages.PriorityUrgent:
		return 2
	case messages.PriorityLow:
		return 0
	default:
		return 1
	}
}

// maxPendingNotifications bounds how many one-shot inter-loop notifications a
// live loop may queue while it is busy or sleeping. enqueueNotify rejects new
// notifications once this cap is reached so a runaway caller cannot grow the
// in-memory pending-notify slice without bound before the loop gets a chance
// to drain it on the next iteration.
const maxPendingNotifications = 8

// maxNotifyEventsInSummary caps how many event-source events are rendered into
// the model-facing notification summary per wake. Source producers should
// already obey messages.MaxLoopEventsPerWake; this remains a defensive cap for
// hand-built LoopNotifyPayload values.
const maxNotifyEventsInSummary = messages.MaxLoopEventsPerWake

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

// WakeTagsFromContext returns the iteration-scoped capability tags
// carried on the envelopes that woke the current loop iteration, if any.
// Handler-based loops can use this to observe source-classified tags
// (for example contacts → "owner", MQTT topic → "security") that the
// loop runtime has already folded into the iteration's Request.InitialTags
// for task-built turns.
func WakeTagsFromContext(ctx context.Context) []string {
	tags, _ := ctx.Value(wakeTagsContextKey{}).([]string)
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, len(tags))
	copy(out, tags)
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

func summarizeNotifyEnvelopes(envs []messages.Envelope) string {
	if len(envs) == 0 {
		return ""
	}
	// Sort by priority descending so urgent wake notifications lead the
	// prompt. Use a stable sort so envelopes that share a priority keep
	// their arrival order, preserving the producer's intent for batches
	// from the same source (e.g. ordered event-source events delivered
	// together).
	ordered := make([]messages.Envelope, len(envs))
	copy(ordered, envs)
	sort.SliceStable(ordered, func(i, j int) bool {
		return priorityRank(ordered[i].Priority) > priorityRank(ordered[j].Priority)
	})
	type notifyView struct {
		ID       string            `json:"id"`
		From     messages.Identity `json:"from"`
		Priority messages.Priority `json:"priority,omitempty"`
		Scope    []string          `json:"scope,omitempty"`
		Payload  map[string]any    `json:"payload,omitempty"`
	}
	views := make([]notifyView, 0, len(ordered))
	for _, env := range ordered {
		payload, _ := decodeLoopNotifyPayload(env.Payload)
		view := notifyView{
			ID:       env.ID,
			From:     env.From,
			Priority: env.Priority,
			Scope:    append([]string(nil), env.Scope...),
		}
		if payload.Kind != "" || strings.TrimSpace(payload.Message) != "" || strings.TrimSpace(payload.Concern) != "" || strings.TrimSpace(payload.SuggestedAction) != "" || strings.TrimSpace(payload.Context) != "" || payload.ForceSupervisor || len(payload.Events) > 0 {
			view.Payload = map[string]any{}
			if strings.TrimSpace(payload.Kind) != "" {
				view.Payload["kind"] = payload.Kind
			}
			// When structured Events are present, Message is a rendered
			// summary of those same events (see RenderLoopEventSummary).
			// Including both doubles the prompt footprint for every wake
			// and risks very large prompts for high-volume sources. The
			// structured Events are the authoritative form; the rendered
			// Message exists for legacy renderers that don't know about
			// Events, and those callers don't read this summary.
			if strings.TrimSpace(payload.Message) != "" && len(payload.Events) == 0 {
				view.Payload["message"] = payload.Message
			}
			if strings.TrimSpace(payload.Concern) != "" {
				view.Payload["concern"] = payload.Concern
			}
			if strings.TrimSpace(payload.SuggestedAction) != "" {
				view.Payload["suggested_action"] = payload.SuggestedAction
			}
			if strings.TrimSpace(payload.Context) != "" {
				view.Payload["context"] = payload.Context
			}
			if payload.ForceSupervisor {
				view.Payload["force_supervisor"] = true
			}
			if len(payload.Events) > 0 {
				// Bound the serialized events so a single wake from a
				// high-volume source (a feed with a long backlog, a
				// repo with many releases between polls) can't blow
				// up the next iteration's prompt. Surface the
				// truncation explicitly so the model can decide whether
				// to drill in via source-specific tools when the wake
				// looks larger than it can fully reason about.
				if len(payload.Events) <= maxNotifyEventsInSummary {
					view.Payload["events"] = payload.Events
				} else {
					view.Payload["events"] = payload.Events[:maxNotifyEventsInSummary]
					view.Payload["events_truncated"] = true
					view.Payload["events_total"] = len(payload.Events)
					view.Payload["events_shown"] = maxNotifyEventsInSummary
				}
			}
		}
		views = append(views, view)
	}
	blob, err := json.Marshal(views)
	if err != nil {
		slog.Warn("loop: failed to summarize notify envelopes", "count", len(views), "error", err)
		return ""
	}
	return "Loop notifications for this run:\n" + string(blob)
}

// FormatNotifyEnvelopes renders one-shot loop notifications for model-facing
// wake context. Task-based loops use this automatically; custom TurnBuilder
// integrations can call it when a notification wake should create an agent
// turn of their own.
func FormatNotifyEnvelopes(envs []messages.Envelope) string {
	return summarizeNotifyEnvelopes(envs)
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
	}
}

func (l *Loop) sleep(ctx context.Context, d time.Duration) bool {
	// Record the scheduled wake instant so loop_status can report when the
	// loop next fires; clear it on wake so a processing loop never reports a
	// stale deadline. A wakeCh notification can cut the sleep short — this is
	// the *scheduled* deadline, not a guarantee.
	l.mu.Lock()
	l.sleepUntil = time.Now().Add(d)
	l.currentSleep = d
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.sleepUntil = time.Time{}
		l.currentSleep = 0
		l.mu.Unlock()
	}()

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	case <-l.wakeCh:
		return true
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
	select {
	case <-ctx.Done():
		return false
	case <-l.wakeCh:
		return true
	}
}
