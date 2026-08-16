package loop

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
)

// This file owns the model-facing projection of inter-loop
// notifications: what a woken loop reads about who poked it, why, and
// what it owes in return. The wake plumbing that queues and drains
// those envelopes lives in signals.go.

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

// maxNotifyEventsInSummary caps how many event-source events are rendered into
// the model-facing notification summary per wake. Source producers should
// already obey messages.MaxLoopEventsPerWake; this remains a defensive cap for
// hand-built LoopNotifyPayload values.
const maxNotifyEventsInSummary = messages.MaxLoopEventsPerWake

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
		ReplyTo  map[string]any    `json:"reply_to,omitempty"`
		Payload  map[string]any    `json:"payload,omitempty"`
	}
	views := make([]notifyView, 0, len(ordered))
	determinationRequested := false
	for _, env := range ordered {
		payload, _ := decodeLoopNotifyPayload(env.Payload)
		// A determination request from an unanswerable sender gets no
		// reply contract: the contract's whole instruction is to wake
		// the requester back, and naming a return path that does not
		// exist is worse than saying nothing.
		determinationRequested = determinationRequested ||
			(requestsDetermination(env, payload) && WakeableLoopSender(env.From))
		view := notifyView{
			ID:       env.ID,
			From:     env.From,
			Priority: env.Priority,
			Scope:    append([]string(nil), env.Scope...),
			ReplyTo:  replyTarget(env.From),
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
	summary := "Loop notifications for this run:\n" + string(blob)
	if determinationRequested {
		summary += "\n\n" + prompts.CoreAttentionReplyContract
	}
	return summary
}

// WakeableLoopSender reports whether an envelope's sender is a loop the
// recipient could wake back. It is the precondition for every part of
// the reply round trip — the rendered reply address, the reply contract,
// and the capability tag that makes loop_wake callable — so the three
// are gated on one predicate and cannot disagree about who is
// answerable.
//
// System, delegate, and interactive senders fail it. They can ask for a
// determination (document-root sync does), but there is no later
// iteration of theirs to deliver one to.
func WakeableLoopSender(from messages.Identity) bool {
	return from.Kind == messages.IdentityLoop && strings.TrimSpace(from.ID) != ""
}

// replyTarget renders the exact loop_wake arguments that reach the
// sender of one notification, or nil when the sender is not a loop the
// recipient can wake back: a delegate, an interactive conversation, or
// system code with no live loop behind it. Go knows that From.ID
// doubles as a loop_wake target; making the model derive that from a
// bare identity block is the difference between a recipient that knows
// it can answer and one that reads the notification as terminal.
//
// A loop that has since exited still renders here. The wake then fails
// with a not-found error that names the target, which teaches the next
// move; suppressing the field would instead teach that no reply was
// ever possible.
func replyTarget(from messages.Identity) map[string]any {
	if !WakeableLoopSender(from) {
		return nil
	}
	target := map[string]any{"tool": "loop_wake", "loop_id": strings.TrimSpace(from.ID)}
	if name := strings.TrimSpace(from.Name); name != "" {
		target["loop_name"] = name
	}
	return target
}

// requestsDetermination reports whether one notification asks its
// recipient to judge something rather than merely telling it a fact.
// [CoreWakeEnvelope] sets both markers, but either alone is enough:
// hand-built envelopes and older senders on the bus carry one or the
// other, and under-rendering the reply contract costs a dropped
// determination while over-rendering costs a paragraph.
func requestsDetermination(env messages.Envelope, payload messages.LoopNotifyPayload) bool {
	for _, s := range env.Scope {
		if strings.TrimSpace(s) == CoreAttentionScope {
			return true
		}
	}
	return strings.TrimSpace(payload.Kind) == CoreAttentionRequestKind
}

// FormatNotifyEnvelopes renders one-shot loop notifications for model-facing
// wake context. Task-based loops use this automatically; custom TurnBuilder
// integrations can call it when a notification wake should create an agent
// turn of their own.
func FormatNotifyEnvelopes(envs []messages.Envelope) string {
	return summarizeNotifyEnvelopes(envs)
}
