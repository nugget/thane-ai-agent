package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

// maxNotifyEventsInSummary caps how many event-source events are rendered into
// the model-facing notification summary per wake. Source producers should
// already obey messages.MaxLoopEventsPerWake; this remains a defensive cap for
// hand-built LoopNotifyPayload values.
const maxNotifyEventsInSummary = messages.MaxLoopEventsPerWake

type pendingNotify struct {
	Envelope        messages.Envelope
	ForceSupervisor bool
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
	type notifyView struct {
		ID       string            `json:"id"`
		From     messages.Identity `json:"from"`
		Priority messages.Priority `json:"priority,omitempty"`
		Scope    []string          `json:"scope,omitempty"`
		Payload  map[string]any    `json:"payload,omitempty"`
	}
	views := make([]notifyView, 0, len(envs))
	for _, env := range envs {
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

	l.pendingNotifies = append(l.pendingNotifies, pendingNotify{
		Envelope:        env,
		ForceSupervisor: payload.ForceSupervisor,
	})
	receipt := NotifyReceipt{
		LoopID:               l.id,
		LoopName:             l.config.Name,
		State:                l.state,
		ForceSupervisor:      payload.ForceSupervisor,
		PendingNotifications: len(l.pendingNotifies),
	}
	if l.state == StateSleeping || l.state == StateWaiting {
		select {
		case l.wakeCh <- struct{}{}:
		default:
		}
		receipt.WokeImmediately = true
	} else {
		receipt.QueuedForNextWake = true
	}
	return receipt, nil
}

func (l *Loop) consumePendingNotifies() ([]messages.Envelope, bool) {
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
		return nil, false
	}
	envs := make([]messages.Envelope, 0, len(l.pendingNotifies))
	forceSupervisor := false
	for _, sig := range l.pendingNotifies {
		envs = append(envs, sig.Envelope)
		forceSupervisor = forceSupervisor || sig.ForceSupervisor
	}
	l.pendingNotifies = nil
	select {
	case <-l.wakeCh:
	default:
	}
	return envs, forceSupervisor
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
