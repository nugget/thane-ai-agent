package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

const (
	queuePullDefaultLimit = 5
	queuePullMaxLimit     = 25
)

// buildLoopQueueTools returns the loop-private work-queue tools for one
// consumer loop. The loop's own name is captured in the closure at
// hydration time and used as the queue partition key, so the model never
// sees or types it and can only ever drain, ack, defer, or enqueue into its own
// partition. Attached via Spec.RuntimeTools, so these tools are
// advertised only on this loop's iterations — never registered globally.
//
// This is the consumer side of the queue-consumer pattern (issue #1024):
// a self-paced loop pulls a batch from its durable inbox, processes it, acks
// what it finished, defers work blocked on external prerequisites, and
// enqueues any newly-discovered subjects.
// Trigger rate (producers calling Enqueue) is fully decoupled from work
// rate (this loop's sleep cadence).
func buildLoopQueueTools(store *loopqueue.Store, loopName string) []looppkg.RuntimeTool {
	receipts := newQueueReceipts()
	return []looppkg.RuntimeTool{
		{
			Name: "queue_pull",
			Description: "Pull a batch of pending work items from your queue, highest priority first then oldest. " +
				"Each item gives a subject (the key you pass to queue_ack), its source, a short summary, priority, and age — not the full payload. " +
				"Items stay queued until you queue_ack them, so an interrupted iteration just re-serves them next time. This is your inbox: drain it; you are never paged.",
			SkipContentResolve: true,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum items to pull this turn (default 5, capped at 25). Pull a batch you can actually process before sleeping.",
					},
				},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				limit, err := intFromMap(args, "limit")
				if err != nil {
					return "", fmt.Errorf("limit: %w", err)
				}
				if limit <= 0 {
					limit = queuePullDefaultLimit
				}
				if limit > queuePullMaxLimit {
					limit = queuePullMaxLimit
				}
				items, err := store.Peek(ctx, loopName, limit)
				if err != nil {
					return "", err
				}
				now := time.Now().UTC()
				views := make([]queueItemView, 0, len(items))
				for _, it := range items {
					receipts.remember(it.DedupKey, it.Receipt)
					source, summary := projectQueuePayload(it.Payload)
					views = append(views, queueItemView{
						Subject:  it.DedupKey,
						Source:   source,
						Summary:  summary,
						Priority: it.Priority,
						Age:      promptfmt.FormatDeltaOnly(it.EnqueuedAt, now),
					})
				}
				return toQueueJSON(queuePullResult{Count: len(views), Items: views})
			},
		},
		{
			Name: "queue_ack",
			Description: "Mark a queue item done and remove the exact item returned by queue_pull. Pass only its subject; Go retains a hidden one-shot receipt and discards it after acknowledgement. " +
				"If newer evidence arrived while you worked, that newer item remains queued and the result tells you to pull it later. Ack only after its evidence is durably folded or you made an evidence-based no-change decision; use queue_defer when an external prerequisite blocks completion.",
			SkipContentResolve: true,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"subject": map[string]any{
						"type":        "string",
						"description": "The subject (queue key) to mark done, exactly as returned by queue_pull (e.g. session:019c..., entity:binary_sensor.foo).",
					},
				},
				"required": []string{"subject"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				subject := strings.TrimSpace(stringMapValue(args, "subject"))
				if subject == "" {
					return "", fmt.Errorf("subject is required")
				}
				receipt, ok := receipts.receipt(subject)
				if !ok {
					return "", fmt.Errorf("subject %q has no receipt from queue_pull in this loop run; call queue_pull before queue_ack", subject)
				}
				outcome, err := store.Ack(ctx, loopName, subject, receipt)
				if err != nil {
					return "", err
				}
				receipts.forget(subject, receipt)
				switch outcome {
				case loopqueue.Acked:
					return fmt.Sprintf(`{"status":"ok","subject":%q}`, subject), nil
				case loopqueue.AckMissing:
					return fmt.Sprintf(`{"status":"already_acknowledged","subject":%q}`, subject), nil
				case loopqueue.AckSuperseded:
					return fmt.Sprintf(`{"status":"retained_newer","subject":%q,"instruction":"Newer evidence arrived while this item was being processed. The newer item remains queued; call queue_pull before handling it."}`, subject), nil
				default:
					return "", fmt.Errorf("unexpected queue acknowledgement outcome %q", outcome)
				}
			},
		},
		{
			Name: "queue_defer",
			Description: "Keep a pulled item pending but move it behind every item currently in your queue. " +
				"Use this when an external prerequisite or an unreadable/truncated source prevents a safe write now. The original evidence remains queued and a future producer can promote it again. Do not defer ordinary no-change work; queue_ack that as complete.",
			SkipContentResolve: true,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"subject": map[string]any{
						"type":        "string",
						"description": "The subject (queue key) to retain for a later iteration, exactly as returned by queue_pull.",
					},
				},
				"required": []string{"subject"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				subject := strings.TrimSpace(stringMapValue(args, "subject"))
				if subject == "" {
					return "", fmt.Errorf("subject is required")
				}
				if err := store.Defer(ctx, loopName, subject); err != nil {
					return "", err
				}
				receipts.forgetSubject(subject)
				return fmt.Sprintf(`{"status":"deferred","subject":%q}`, subject), nil
			},
		},
		{
			Name: "queue_enqueue",
			Description: "Add a subject to your own queue for a future iteration — this is how you expand the frontier (a related entity, a sibling dossier worth refreshing) without spawning anything. " +
				"Coalesces: enqueuing a subject already pending just refreshes it, so the same discovery from two angles can't pile up. You are a single self-paced consumer, so enqueue is the only way you make more work for yourself, and it can never run away.",
			SkipContentResolve: true,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"subject": map[string]any{
						"type":        "string",
						"description": "The subject to enqueue, e.g. entity:binary_sensor.foo, area:garage, contact:019c... Use the same subject:<id> shape you receive from queue_pull.",
					},
					"reason": map[string]any{
						"type":        "string",
						"description": "Optional one-line why, surfaced back to you when you pull this item (e.g. 'co-occurs with garage door pattern').",
					},
					"priority": map[string]any{
						"type":        "integer",
						"description": "Optional priority (higher drains first; default 0).",
					},
				},
				"required": []string{"subject"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				subject := strings.TrimSpace(stringMapValue(args, "subject"))
				if subject == "" {
					return "", fmt.Errorf("subject is required")
				}
				reason := strings.TrimSpace(stringMapValue(args, "reason"))
				priority, err := intFromMap(args, "priority")
				if err != nil {
					return "", fmt.Errorf("priority: %w", err)
				}
				payload := messages.LoopNotifyPayload{
					Events: []messages.LoopEventPayload{{
						Source:     "frontier",
						Type:       "queue_item",
						ID:         subject,
						Summary:    reason,
						ObservedAt: time.Now().UTC(),
					}},
				}
				raw, err := json.Marshal(payload)
				if err != nil {
					return "", fmt.Errorf("marshal payload: %w", err)
				}
				if err := store.Enqueue(ctx, loopName, subject, priority, raw); err != nil {
					return "", err
				}
				return fmt.Sprintf(`{"status":"ok","subject":%q}`, subject), nil
			},
		},
	}
}

type queueReceipts struct {
	mu        sync.Mutex
	bySubject map[string]string
}

func newQueueReceipts() *queueReceipts {
	return &queueReceipts{bySubject: make(map[string]string)}
}

func (r *queueReceipts) remember(subject, receipt string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.bySubject[subject]; ok {
		return
	}
	r.bySubject[subject] = receipt
}

func (r *queueReceipts) receipt(subject string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	receipt, ok := r.bySubject[subject]
	return receipt, ok
}

func (r *queueReceipts) forget(subject, expectedReceipt string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if receipt, ok := r.bySubject[subject]; ok && receipt == expectedReceipt {
		delete(r.bySubject, subject)
	}
}

func (r *queueReceipts) forgetSubject(subject string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.bySubject, subject)
}

type queueItemView struct {
	Subject  string `json:"subject"`
	Source   string `json:"source,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Priority int    `json:"priority"`
	Age      string `json:"age,omitempty"`
}

type queuePullResult struct {
	Count int             `json:"count"`
	Items []queueItemView `json:"items"`
}

// projectQueuePayload extracts the model-facing source + summary from a
// stored LoopNotifyPayload without exposing the whole payload. Falls back
// across the payload's discriminator/message fields so producers that set
// either the event facts or the notify body both render usefully.
func projectQueuePayload(raw []byte) (source, summary string) {
	var p messages.LoopNotifyPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", ""
	}
	if len(p.Events) > 0 {
		e := p.Events[0]
		source = e.Source
		summary = e.Summary
		if summary == "" {
			summary = e.Title
		}
	}
	if source == "" {
		source = p.Kind
	}
	if summary == "" {
		summary = p.Message
	}
	return source, summary
}

func toQueueJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal queue result: %w", err)
	}
	return string(b), nil
}
