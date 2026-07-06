package tools

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

const (
	defaultHATraceRunLimit        = 10
	maxHATraceRunLimit            = 25
	haTracesSummaryTruncationNote = "Result exceeded the tool byte cap; pass run_id for one run's detail, or lower limit."
	haTracesDetailTruncationNote  = "Run detail exceeded the tool byte cap; later steps were cut from this preview. The run block at the top (last_step, error) still shows where the run ended."
	haTraceVariablesOmittedNote   = "Step variable snapshots are omitted; results and errors are shown per step."
)

// haTraceRunsResult is the summary shape: recent runs of one
// automation, newest first.
type haTraceRunsResult struct {
	Automation string           `json:"automation"`
	ID         string           `json:"id"`
	Count      int              `json:"count"`
	Truncated  bool             `json:"truncated,omitempty"`
	Note       string           `json:"note,omitempty"`
	Runs       []haTraceRunView `json:"runs"`
}

type haTraceRunView struct {
	RunID       string `json:"run_id"`
	Started     string `json:"started"`
	DurationMS  int64  `json:"duration_ms,omitempty"`
	State       string `json:"state"`
	Execution   string `json:"execution,omitempty"`
	TriggeredBy string `json:"triggered_by,omitempty"`
	LastStep    string `json:"last_step,omitempty"`
	Error       string `json:"error,omitempty"`
}

// haTraceDetailResult is the run-scoped shape: the step-by-step record
// of one run, in execution order.
type haTraceDetailResult struct {
	Automation string            `json:"automation"`
	ID         string            `json:"id"`
	Run        haTraceRunView    `json:"run"`
	StepCount  int               `json:"step_count"`
	Truncated  bool              `json:"truncated,omitempty"`
	Note       string            `json:"note"`
	Steps      []haTraceStepView `json:"steps"`
}

type haTraceStepView struct {
	Path   string         `json:"path"`
	At     string         `json:"at"`
	Result map[string]any `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// registerHAAutomationTraces wires ha_automation_traces: the debugging
// half of the automation family. Create/update/get answer "what is the
// automation"; traces answer "what did it actually do when it ran" —
// which trigger fired, which conditions passed, what each action did.
// Requires the WebSocket client, like the rest of the automation family.
func (r *Registry) registerHAAutomationTraces() {
	if r.ha == nil || !r.ha.HasWSClient() {
		return
	}
	r.Register(&Tool{
		Name: "ha_automation_traces",
		Description: "Inspect recent runs of a Home Assistant automation — the step-by-step record of what actually happened. " +
			"Without run_id: recent runs newest-first (when, what triggered it, how it ended, any error). " +
			"With run_id: that run's full step trace in execution order, with per-step results and errors. " +
			"Home Assistant keeps only a handful of recent runs per automation; an empty list usually means it hasn't run recently. " +
			"Use after ha_automation_create/update to verify behavior, or when an automation misfires.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "Automation config ID. Provide this or entity_id.",
				},
				"entity_id": map[string]any{
					"type":        "string",
					"description": "Automation entity_id (automation.*). Provide this or id.",
				},
				"run_id": map[string]any{
					"type":        "string",
					"description": "A run_id from the summary listing; returns that run's full step trace.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Max runs in the summary listing (default 10, max 25).",
				},
			},
		},
		Handler: r.handleHAAutomationTraces,
	})
}

func (r *Registry) handleHAAutomationTraces(ctx context.Context, args map[string]any) (string, error) {
	if r.ha == nil {
		return "", fmt.Errorf("home assistant not configured")
	}
	if !r.ha.IsReady() {
		return "", fmt.Errorf("home assistant is currently unreachable (reconnecting in background)")
	}

	idArg, _ := args["id"].(string)
	entityArg, _ := args["entity_id"].(string)
	runID, _ := args["run_id"].(string)

	resolved, err := r.resolveAutomation(ctx, idArg, entityArg)
	if err != nil {
		return "", err
	}

	limit := defaultHATraceRunLimit
	if l, ok := args["limit"].(float64); ok && int(l) > 0 {
		limit = int(l)
	}
	if limit > maxHATraceRunLimit {
		limit = maxHATraceRunLimit
	}

	now := time.Now()
	if runID != "" {
		detail, err := r.ha.GetAutomationTrace(ctx, resolved.id, runID)
		if err != nil {
			return "", fmt.Errorf("get trace %q: %w (run_ids come from the summary listing; traces expire as new runs arrive)", runID, err)
		}
		return haTraceDetailView(resolved, detail, now), nil
	}

	runs, err := r.ha.ListAutomationTraces(ctx, resolved.id)
	if err != nil {
		return "", err
	}
	return haTraceRunsView(resolved, runs, limit, now), nil
}

func haTraceRunsView(resolved resolvedAutomation, runs []homeassistant.AutomationTraceSummary, limit int, now time.Time) string {
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].Timestamp.Start.After(runs[j].Timestamp.Start)
	})
	total := len(runs)
	if len(runs) > limit {
		runs = runs[:limit]
	}

	out := haTraceRunsResult{
		Automation: resolved.entityID,
		ID:         resolved.id,
		Count:      len(runs),
		Truncated:  total > len(runs),
		Runs:       make([]haTraceRunView, 0, len(runs)),
	}
	if total == 0 {
		out.Note = "No stored runs. Home Assistant keeps traces only for recent runs; this usually means the automation hasn't fired lately."
	}
	for _, run := range runs {
		out.Runs = append(out.Runs, haTraceRunSummaryView(run, now))
	}
	return toIndentedJSONWithTruncationNote(out, haTracesSummaryTruncationNote)
}

func haTraceRunSummaryView(run homeassistant.AutomationTraceSummary, now time.Time) haTraceRunView {
	view := haTraceRunView{
		RunID:       run.RunID,
		Started:     promptfmt.FormatDeltaOnly(run.Timestamp.Start, now),
		State:       run.State,
		Execution:   run.ScriptExecution,
		TriggeredBy: run.Trigger,
		LastStep:    run.LastStep,
		Error:       run.Error,
	}
	if run.Timestamp.Finish != nil {
		view.DurationMS = run.Timestamp.Finish.Sub(run.Timestamp.Start).Milliseconds()
	}
	return view
}

func haTraceDetailView(resolved resolvedAutomation, detail *homeassistant.AutomationTraceDetail, now time.Time) string {
	// Collect with real timestamps first: execution order must come
	// from the instants, not from projected delta strings (which do
	// not compare temporally) and not from the path-keyed map order.
	type timedStep struct {
		at   time.Time
		view haTraceStepView
	}
	timed := make([]timedStep, 0, len(detail.Trace)*2)
	for path, executions := range detail.Trace {
		for _, step := range executions {
			stepPath := step.Path
			if stepPath == "" {
				stepPath = path
			}
			timed = append(timed, timedStep{
				at: step.Timestamp,
				view: haTraceStepView{
					Path:   stepPath,
					At:     promptfmt.FormatDeltaOnly(step.Timestamp, now),
					Result: step.Result,
					Error:  step.Error,
				},
			})
		}
	}
	sort.Slice(timed, func(i, j int) bool {
		if !timed[i].at.Equal(timed[j].at) {
			return timed[i].at.Before(timed[j].at)
		}
		return timed[i].view.Path < timed[j].view.Path
	})
	steps := make([]haTraceStepView, len(timed))
	for i, ts := range timed {
		steps[i] = ts.view
	}

	out := haTraceDetailResult{
		Automation: resolved.entityID,
		ID:         resolved.id,
		Run:        haTraceRunSummaryView(detail.AutomationTraceSummary, now),
		StepCount:  len(steps),
		Note:       haTraceVariablesOmittedNote,
		Steps:      steps,
	}
	return toIndentedJSONWithTruncationNote(out, haTracesDetailTruncationNote)
}
