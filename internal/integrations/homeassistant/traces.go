package homeassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// TraceTimestamps carries a trace run's start and finish instants.
// Finish is nil while a run is still executing.
type TraceTimestamps struct {
	Start  time.Time  `json:"start"`
	Finish *time.Time `json:"finish"`
}

// AutomationTraceSummary is one run's row from the trace/list WebSocket
// command: enough to see when the automation ran, what set it off, and
// how the run ended, without the step-by-step tree.
type AutomationTraceSummary struct {
	// RunID identifies the run; pass it to GetAutomationTrace for the
	// full step tree.
	RunID string `json:"run_id"`

	// State is the run's lifecycle state ("running" or "stopped").
	State string `json:"state"`

	// ScriptExecution reports how execution concluded — "finished",
	// "failed_conditions", "cancelled", "error", and friends. Empty
	// while the run is still in flight.
	ScriptExecution string `json:"script_execution"`

	// Timestamp carries the run's start/finish instants.
	Timestamp TraceTimestamps `json:"timestamp"`

	// Trigger is HA's human-readable description of what fired the
	// run (e.g. "state of binary_sensor.office_motion").
	Trigger string `json:"trigger"`

	// LastStep is the path of the last step the run reached.
	LastStep string `json:"last_step"`

	// Error carries the run-level error message when the run aborted.
	Error string `json:"error"`
}

// TraceStep is one recorded step execution inside a trace tree.
type TraceStep struct {
	// Path locates the step in the automation config (e.g. "trigger/0",
	// "condition/1", "action/0/choose/1/sequence/0").
	Path string `json:"path"`

	// Timestamp is when the step executed.
	Timestamp time.Time `json:"timestamp"`

	// Result is the step's recorded outcome (shape varies by step
	// kind: condition results carry a boolean, service steps carry
	// call parameters).
	Result map[string]any `json:"result,omitempty"`

	// Error carries the step's error message when it failed.
	Error string `json:"error"`

	// ChangedVariables is the raw variable snapshot at this step. It
	// is bulky and rarely worth shipping to a model verbatim; the
	// traces tool omits it from projections.
	ChangedVariables json.RawMessage `json:"changed_variables,omitempty"`
}

// AutomationTraceDetail is the trace/get result: the run summary plus
// the full step tree keyed by config path.
type AutomationTraceDetail struct {
	AutomationTraceSummary

	// Trace maps each executed config path to its step executions, in
	// execution order per path (a path repeats when a loop re-enters it).
	Trace map[string][]TraceStep `json:"trace"`
}

// ListAutomationTraces retrieves recent run summaries for one
// automation, identified by its config item id. HA retains only a
// handful of recent runs per automation; an empty result usually means
// the automation hasn't run recently, not that it never ran.
func (c *Client) ListAutomationTraces(ctx context.Context, itemID string) ([]AutomationTraceSummary, error) {
	ws, err := c.requireWS()
	if err != nil {
		return nil, err
	}
	return ws.ListAutomationTraces(ctx, itemID)
}

// GetAutomationTrace retrieves one run's full step tree.
func (c *Client) GetAutomationTrace(ctx context.Context, itemID, runID string) (*AutomationTraceDetail, error) {
	ws, err := c.requireWS()
	if err != nil {
		return nil, err
	}
	return ws.GetAutomationTrace(ctx, itemID, runID)
}

// ListAutomationTraces implements the trace/list WebSocket command for
// automations.
func (c *WSClient) ListAutomationTraces(ctx context.Context, itemID string) ([]AutomationTraceSummary, error) {
	var result []AutomationTraceSummary
	if err := c.call(ctx, "trace/list", map[string]any{"domain": "automation", "item_id": itemID}, &result); err != nil {
		return nil, fmt.Errorf("list automation traces: %w", err)
	}
	return result, nil
}

// GetAutomationTrace implements the trace/get WebSocket command for
// automations.
func (c *WSClient) GetAutomationTrace(ctx context.Context, itemID, runID string) (*AutomationTraceDetail, error) {
	var result AutomationTraceDetail
	if err := c.call(ctx, "trace/get", map[string]any{"domain": "automation", "item_id": itemID, "run_id": runID}, &result); err != nil {
		return nil, fmt.Errorf("get automation trace: %w", err)
	}
	return &result, nil
}
