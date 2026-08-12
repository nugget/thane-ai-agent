package introspection

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
	"github.com/nugget/thane-ai-agent/internal/tools"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// ToolDeps wires the diagnostics tool family. Every dependency is
// optional: the tools are always declared (so capability-tag resolution
// sees them), and a handler whose backing runtime is missing returns
// [tools.ErrUnavailable] naming what is unwired.
type ToolDeps struct {
	Inspector *Inspector
	Journal   *Journal
	Documents *documents.Store
	Queue     *loopqueue.Store
}

// Tools is the diagnostics tool family provider: system_health,
// queue_status, doc_activity, loop_activity — the read-only
// internal-operations surface behind the metacog re-scope (#1341).
type Tools struct {
	deps ToolDeps
}

// NewTools builds the diagnostics tool provider.
func NewTools(deps ToolDeps) *Tools {
	return &Tools{deps: deps}
}

// Name implements [tools.Provider].
func (t *Tools) Name() string { return "introspection.diagnostics" }

const (
	defaultActivityWindow  = "-24h"
	maxActivityWindow      = 30 * 24 * time.Hour
	defaultCompletionLimit = 20
	maxCompletionLimit     = 100
)

// Tools implements [tools.Provider].
func (t *Tools) Tools() []*tools.Tool {
	windowParam := func(what string) map[string]any {
		return map[string]any{
			"type":        "string",
			"description": fmt.Sprintf("Window start for %s as a signed delta from now (e.g. \"-24h\", \"-7d\") or an RFC3339 timestamp. Default %q; the journal retains about 30 days.", what, defaultActivityWindow),
		}
	}
	return []*tools.Tool{
		{
			Name: "system_health",
			Description: "The annunciator panel for thane's own internals: one status row per subsystem (ok / degraded / failed, with a precomputed detail sentence) covering external connections, the memory guard, event-bus loss, log-index loss, document-root sync, work-queue backlog, and the loop fleet — plus host basics (disk, goroutines, uptime), per-partition queue depths, a 24h telemetry rollup (requests, errors, latency p50/p95, database sizes), the deploy story (running vs previous version, when the boundary landed, size of the jump, recent boots), and the process's own WARN/ERROR rates with the newest samples. " +
				"Zero arguments; call it first when anything feels off. Each degraded row names the subsystem the drill-down tools filter by: loop problems → loop_status / loop_activity, queue backlog → queue_status, log or error detail → logs_query, document churn → doc_activity.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
			Handler:    t.handleSystemHealth,
		},
		{
			Name: "queue_status",
			Description: "Inspect every durable work-queue partition, globally and read-only: live pending depth with oldest-item age per consumer loop, completion statistics over a window (count, average and max enqueue→ack wait), and the most recent completions. " +
				"This is the audit view — you cannot drain or ack from here; consumer loops own their own partitions via their private queue tools. " +
				"Note: a coalescing re-enqueue refreshes a pending item in place and never appears as a completion, so completion counts measure consumer throughput, not producer chatter.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"consumer": map[string]any{
						"type":        "string",
						"description": "Optional exact partition name to scope recent completions to — consumer names as shown in this tool's own pending/completion rows (loop names, plus internal wake partitions with prefixes like subwake: or mqttwake:).",
					},
					"window":            windowParam("completion statistics"),
					"completions_limit": map[string]any{"type": "integer", "description": fmt.Sprintf("Maximum recent completions to return (integer; default %d, max %d).", defaultCompletionLimit, maxCompletionLimit)},
				},
			},
			Handler: t.handleQueueStatus,
		},
		{
			Name: "doc_activity",
			Description: "Revision-churn report over the managed document roots: per document in the window, the revision count, net line delta (one spanning diff), current size, and authorship from commit trailers — rows authored \"manual\" were not written by a loop. " +
				"Documents rewriting themselves at or past the threshold are flagged and sort first: a runaway maintained document (an ego.md accumulating nonsense) shows up here before anyone reads it. " +
				"Covers git-backed roots only; drill into a specific document's history with doc_history / doc_diff.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"root": map[string]any{
						"type":        "string",
						"description": "Optional root name (e.g. \"self\", \"core\"). Omit to sweep every revision-backed root.",
					},
					"window": windowParam("the churn report"),
					"limit":  map[string]any{"type": "integer", "description": "Maximum documents per root (integer; default 20, max 100). Flagged documents always sort first, so the cap cannot hide a runaway."},
					"threshold": map[string]any{
						"type":        "integer",
						"description": "Revisions-in-window at which a document is flagged as a runaway (integer; default 8).",
					},
				},
			},
			Handler: t.handleDocActivity,
		},
		{
			Name: "loop_activity",
			Description: "The loop fleet's history, from the persistent event journal: loop_status is the now-snapshot, loop_activity is what actually happened — every wake with its attributed cause (timer, mailbox, subscription, manual loop_wake, notify — and who sent it), iteration outcomes, errors, and state changes, surviving restarts and covering loops that have since stopped. " +
				"The aggregate leads: wake volume and rate, the by-reason and by-source decomposition (who keeps waking this loop), error and completion counts. no_ops counts the completions that changed nothing — a subset of completions, not a sibling outcome, so never sum the two. Use it to spot wake storms, dead cadences, and loops going through the motions.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"loop_name": map[string]any{
						"type":        "string",
						"description": "Optional exact loop name. Omit for the whole fleet.",
					},
					"kind": map[string]any{
						"type":        "string",
						"enum":        []string{"loop_started", "loop_stopped", "loop_iteration_start", "loop_iteration_complete", "loop_error", "loop_state_change", "loop_mailbox_arrival", "loop_midturn_input", "thane_boot"},
						"description": "Optional event-kind filter. loop_iteration_start rows carry the wake attribution; thane_boot rows mark process starts with the build version, so deploy boundaries and restarts are queryable history.",
					},
					"since": windowParam("the activity window"),
					"until": map[string]any{
						"type":        "string",
						"description": "Optional window end, same forms as since. Omit for now.",
					},
					"limit": map[string]any{"type": "integer", "description": "Maximum events to return (integer; default 50, max 200). The newest events win the cap; the aggregate always covers the whole window."},
				},
			},
			Handler: t.handleLoopActivity,
		},
	}
}

func marshalToolResult(payload any) (string, error) {
	blob, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal tool result: %w", err)
	}
	return string(blob), nil
}

// parseWindowArg resolves a delta-or-RFC3339 window argument, bounded
// by the journal's retention so a huge window doesn't imply data that
// was already pruned.
func parseWindowArg(args map[string]any, key, fallback string, now time.Time) (time.Time, error) {
	raw := toolargs.TrimmedString(args, key)
	if raw == "" {
		raw = fallback
	}
	at, err := promptfmt.ParseTimeOrDelta(raw, now)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s %q: use a signed delta like \"-24h\" or an RFC3339 timestamp", key, raw)
	}
	if floor := now.Add(-maxActivityWindow); at.Before(floor) {
		at = floor
	}
	return at, nil
}

func (t *Tools) handleSystemHealth(ctx context.Context, _ map[string]any) (string, error) {
	if t.deps.Inspector == nil {
		return "", tools.ErrUnavailable{Tool: "system_health", Reason: "health inspector is not wired"}
	}
	// The tool returns the same flat payload the metacog panel renders
	// (snapshotPayload) — one projection, so a model reading the panel
	// one iteration and this tool the next sees one shape, not two.
	return marshalToolResult(snapshotPayload(t.deps.Inspector.Health(ctx)))
}

func (t *Tools) handleQueueStatus(ctx context.Context, args map[string]any) (string, error) {
	if t.deps.Queue == nil {
		return "", tools.ErrUnavailable{Tool: "queue_status", Reason: "loop work queue is not wired"}
	}
	now := time.Now()
	since, err := parseWindowArg(args, "window", defaultActivityWindow, now)
	if err != nil {
		return "", err
	}
	consumer := toolargs.TrimmedString(args, "consumer")
	limit := toolargs.IntOr(args, "completions_limit", defaultCompletionLimit)
	if limit <= 0 {
		limit = defaultCompletionLimit
	}
	if limit > maxCompletionLimit {
		limit = maxCompletionLimit
	}

	pending, err := t.deps.Queue.PendingStats(ctx)
	if err != nil {
		return "", fmt.Errorf("read pending stats: %w", err)
	}
	// Rendered through the same projection the health snapshot's queues
	// section uses, so the two surfaces can never age the same
	// partition differently.
	pendingRows := queueDepths(pending, now)

	stats, err := t.deps.Queue.CompletionStatsSince(ctx, since)
	if err != nil {
		return "", fmt.Errorf("read completion stats: %w", err)
	}
	type statsRow struct {
		Consumer string `json:"consumer"`
		Count    int    `json:"count"`
		AvgWait  string `json:"avg_wait"`
		MaxWait  string `json:"max_wait"`
	}
	statsRows := make([]statsRow, 0, len(stats))
	for _, cs := range stats {
		statsRows = append(statsRows, statsRow{
			Consumer: cs.Consumer,
			Count:    cs.Count,
			AvgWait:  promptfmt.FormatDuration(cs.AvgWait.Round(time.Second)),
			MaxWait:  promptfmt.FormatDuration(cs.MaxWait.Round(time.Second)),
		})
	}

	completions, err := t.deps.Queue.RecentCompletions(ctx, consumer, since, limit)
	if err != nil {
		return "", fmt.Errorf("read recent completions: %w", err)
	}
	type completionRow struct {
		Consumer   string `json:"consumer"`
		Subject    string `json:"subject"`
		Waited     string `json:"waited"`
		AckedDelta string `json:"acked_delta"`
	}
	completionRows := make([]completionRow, 0, len(completions))
	for _, c := range completions {
		completionRows = append(completionRows, completionRow{
			Consumer:   c.Consumer,
			Subject:    c.DedupKey,
			Waited:     promptfmt.FormatDuration(c.Waited().Round(time.Second)),
			AckedDelta: promptfmt.FormatDeltaOnly(c.AckedAt, now),
		})
	}

	return marshalToolResult(map[string]any{
		"window_start":       promptfmt.FormatDeltaOnly(since, now),
		"pending":            pendingRows,
		"completion_stats":   statsRows,
		"recent_completions": completionRows,
		"note":               "coalesced re-enqueues refresh a pending item in place and do not appear as completions",
	})
}

func (t *Tools) handleDocActivity(ctx context.Context, args map[string]any) (string, error) {
	if t.deps.Documents == nil {
		return "", tools.ErrUnavailable{Tool: "doc_activity", Reason: "document store is not wired"}
	}
	now := time.Now()
	since, err := parseWindowArg(args, "window", defaultActivityWindow, now)
	if err != nil {
		return "", err
	}
	limit := toolargs.Int(args, "limit")
	threshold := toolargs.Int(args, "threshold")

	roots := []string{toolargs.TrimmedString(args, "root")}
	if roots[0] == "" {
		roots = t.deps.Documents.RevisionBackedRoots()
		if len(roots) == 0 {
			return "", fmt.Errorf("no document root keeps revision history; doc_activity covers git-backed roots only")
		}
	}

	reports := make([]any, 0, len(roots))
	for _, root := range roots {
		report, err := t.deps.Documents.Activity(ctx, documents.ActivityQuery{
			Root:              root,
			Since:             since,
			Limit:             limit,
			RevisionThreshold: threshold,
		})
		if err != nil {
			// A named root's failure is the caller's answer; a sweep
			// reports the failing root explicitly and keeps going.
			if len(roots) == 1 {
				return "", err
			}
			reports = append(reports, map[string]any{"root": root, "error": err.Error()})
			continue
		}
		reports = append(reports, report)
	}
	return marshalToolResult(map[string]any{
		"window_start": promptfmt.FormatDeltaOnly(since, now),
		"reports":      reports,
	})
}

func (t *Tools) handleLoopActivity(ctx context.Context, args map[string]any) (string, error) {
	if t.deps.Journal == nil {
		return "", tools.ErrUnavailable{Tool: "loop_activity", Reason: "loop event journal is not wired (logs.db unavailable)"}
	}
	now := time.Now()
	since, err := parseWindowArg(args, "since", defaultActivityWindow, now)
	if err != nil {
		return "", err
	}
	until := now
	if raw := toolargs.TrimmedString(args, "until"); raw != "" {
		until, err = promptfmt.ParseTimeOrDelta(raw, now)
		if err != nil {
			return "", fmt.Errorf("invalid until %q: use a signed delta like \"-1h\" or an RFC3339 timestamp", raw)
		}
		// The journal only holds the past: a future until is clamped to
		// now, and an inverted window is a caller error worth teaching
		// rather than an empty result worth guessing about.
		if until.After(now) {
			until = now
		}
		if until.Before(since) {
			return "", fmt.Errorf("until %q is before since (window start %s): widen since or move until later", raw, promptfmt.FormatDeltaOnly(since, now))
		}
	}
	loopName := toolargs.TrimmedString(args, "loop_name")
	kind := toolargs.TrimmedString(args, "kind")
	limit := toolargs.IntOr(args, "limit", defaultLoopEventLimit)
	if limit <= 0 {
		limit = defaultLoopEventLimit
	}
	if limit > maxLoopEventLimit {
		limit = maxLoopEventLimit
	}

	agg, err := t.deps.Journal.AggregateActivity(ctx, loopName, since, until)
	if err != nil {
		return "", err
	}
	events, err := t.deps.Journal.Query(ctx, LoopEventQuery{
		LoopName: loopName,
		Kind:     kind,
		Since:    since,
		Until:    until,
		Limit:    limit,
	})
	if err != nil {
		return "", err
	}

	type eventRow struct {
		AtDelta    string         `json:"at_delta"`
		LoopName   string         `json:"loop_name,omitempty"`
		Kind       string         `json:"kind"`
		WakeReason string         `json:"wake_reason,omitempty"`
		WakeSource string         `json:"wake_source,omitempty"`
		Detail     map[string]any `json:"detail,omitempty"`
	}
	rows := make([]eventRow, 0, len(events))
	for _, ev := range events {
		rows = append(rows, eventRow{
			AtDelta:    promptfmt.FormatDeltaOnly(ev.At, now),
			LoopName:   ev.LoopName,
			Kind:       ev.Kind,
			WakeReason: ev.WakeReason,
			WakeSource: ev.WakeSource,
			Detail:     ev.Detail,
		})
	}

	return marshalToolResult(map[string]any{
		"window_start": promptfmt.FormatDeltaOnly(since, now),
		"aggregate": map[string]any{
			"wakes":          agg.Wakes,
			"wakes_per_hour": fmt.Sprintf("%.2f", agg.WakesPerHour),
			"by_reason":      agg.ByReason,
			"by_source":      agg.BySource,
			"errors":         agg.Errors,
			"completions":    agg.Completions,
			"no_ops":         agg.NoOps,
		},
		"events":        rows,
		"limit_reached": len(rows) > 0 && len(rows) >= limit,
	})
}
