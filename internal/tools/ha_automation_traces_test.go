package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// traceAutomationState seeds one automation entity so resolveAutomation
// can map entity_id → config id.
func traceAutomationState() homeassistant.State {
	return homeassistant.State{
		EntityID: "automation.office_lights",
		State:    "on",
		Attributes: map[string]any{
			"friendly_name": "Office lights",
			"id":            "auto-42",
		},
	}
}

func traceFixtureRuns(now time.Time) []map[string]any {
	ts := func(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
	return []map[string]any{
		{
			"run_id":           "run-old",
			"state":            "stopped",
			"script_execution": "finished",
			"timestamp":        map[string]any{"start": ts(now.Add(-2 * time.Hour)), "finish": ts(now.Add(-2*time.Hour + 3*time.Second))},
			"trigger":          "state of binary_sensor.office_motion",
			"last_step":        "action/0",
		},
		{
			"run_id":           "run-new",
			"state":            "stopped",
			"script_execution": "failed_conditions",
			"timestamp":        map[string]any{"start": ts(now.Add(-5 * time.Minute)), "finish": ts(now.Add(-5*time.Minute + 40*time.Millisecond))},
			"trigger":          "time pattern",
			"last_step":        "condition/0",
		},
	}
}

func TestHAAutomationTraces_SummaryNewestFirst(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{traceAutomationState()}
	fake.configs["auto-42"] = map[string]any{"alias": "Office lights"}
	now := time.Now()
	fake.traces = map[string][]map[string]any{"auto-42": traceFixtureRuns(now)}
	reg := fake.registry(t)

	raw, err := reg.Execute(context.Background(), "ha_automation_traces", `{"entity_id":"automation.office_lights"}`)
	if err != nil {
		t.Fatalf("ha_automation_traces: %v", err)
	}
	var out haTraceRunsResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if out.Automation != "automation.office_lights" || out.ID != "auto-42" {
		t.Fatalf("identity = %q/%q, want automation.office_lights/auto-42", out.Automation, out.ID)
	}
	if out.Count != 2 || len(out.Runs) != 2 {
		t.Fatalf("count = %d/%d runs, want 2", out.Count, len(out.Runs))
	}
	if out.Runs[0].RunID != "run-new" || out.Runs[1].RunID != "run-old" {
		t.Errorf("order = %q,%q, want newest first (run-new, run-old)", out.Runs[0].RunID, out.Runs[1].RunID)
	}
	if out.Runs[0].Execution != "failed_conditions" || out.Runs[0].TriggeredBy != "time pattern" {
		t.Errorf("run view lost outcome/trigger: %+v", out.Runs[0])
	}
	if out.Runs[1].DurationMS != 3000 {
		t.Errorf("duration_ms = %d, want 3000", out.Runs[1].DurationMS)
	}
	if out.Runs[0].Started == "" || out.Runs[0].Started[0] != '-' {
		t.Errorf("started should be a past delta, got %q", out.Runs[0].Started)
	}
}

func TestHAAutomationTraces_LimitAndTruncation(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{traceAutomationState()}
	fake.configs["auto-42"] = map[string]any{"alias": "Office lights"}
	fake.traces = map[string][]map[string]any{"auto-42": traceFixtureRuns(time.Now())}
	reg := fake.registry(t)

	raw, err := reg.Execute(context.Background(), "ha_automation_traces", `{"entity_id":"automation.office_lights","limit":1}`)
	if err != nil {
		t.Fatalf("limited: %v", err)
	}
	var out haTraceRunsResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Count != 1 || !out.Truncated {
		t.Errorf("count/truncated = %d/%v, want 1/true", out.Count, out.Truncated)
	}
}

func TestHAAutomationTraces_DetailStepsInExecutionOrder(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{traceAutomationState()}
	fake.configs["auto-42"] = map[string]any{"alias": "Office lights"}
	now := time.Now()
	ts := func(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
	start := now.Add(-10 * time.Minute)
	fake.traces = map[string][]map[string]any{"auto-42": traceFixtureRuns(now)}
	fake.traceDetails = map[string]map[string]any{
		"run-new": {
			"run_id":           "run-new",
			"state":            "stopped",
			"script_execution": "finished",
			"timestamp":        map[string]any{"start": ts(start), "finish": ts(start.Add(2 * time.Second))},
			"trigger":          "time pattern",
			"last_step":        "action/1",
			"trace": map[string]any{
				// Deliberately listed with the LATER path first: order
				// must come from timestamps, not map or path order.
				"action/1": []map[string]any{
					{"path": "action/1", "timestamp": ts(start.Add(2 * time.Second)), "error": "service unavailable"},
				},
				"trigger/0": []map[string]any{
					{"path": "trigger/0", "timestamp": ts(start)},
				},
				"condition/0": []map[string]any{
					{"path": "condition/0", "timestamp": ts(start.Add(time.Second)), "result": map[string]any{"result": true}},
				},
			},
		},
	}
	reg := fake.registry(t)

	raw, err := reg.Execute(context.Background(), "ha_automation_traces", `{"entity_id":"automation.office_lights","run_id":"run-new"}`)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	var out haTraceDetailResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if out.StepCount != 3 || len(out.Steps) != 3 {
		t.Fatalf("step count = %d/%d, want 3", out.StepCount, len(out.Steps))
	}
	wantOrder := []string{"trigger/0", "condition/0", "action/1"}
	for i, want := range wantOrder {
		if out.Steps[i].Path != want {
			t.Fatalf("steps[%d] = %q, want %q (execution order by timestamp)", i, out.Steps[i].Path, want)
		}
	}
	if out.Steps[1].Result == nil {
		t.Error("condition step lost its result")
	}
	if out.Steps[2].Error != "service unavailable" {
		t.Errorf("action step error = %q, want surfaced", out.Steps[2].Error)
	}
	if out.Note == "" {
		t.Error("detail must carry the variables-omitted note")
	}
}

func TestHAAutomationTraces_Errors(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{traceAutomationState()}
	fake.configs["auto-42"] = map[string]any{"alias": "Office lights"}
	fake.traces = map[string][]map[string]any{"auto-42": {}}
	reg := fake.registry(t)

	// Unknown run_id: the fake returns no result → tool error teaches
	// where run_ids come from.
	if _, err := reg.Execute(context.Background(), "ha_automation_traces", `{"entity_id":"automation.office_lights","run_id":"nope"}`); err == nil {
		t.Error("unknown run_id: expected error")
	}
	// No automation reference at all.
	if _, err := reg.Execute(context.Background(), "ha_automation_traces", `{}`); err == nil {
		t.Error("missing id/entity_id: expected error")
	}
	// Empty trace list is a valid, non-error result with the teaching note.
	raw, err := reg.Execute(context.Background(), "ha_automation_traces", `{"entity_id":"automation.office_lights"}`)
	if err != nil {
		t.Fatalf("empty list should not error: %v", err)
	}
	var out haTraceRunsResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Count != 0 || out.Note == "" {
		t.Errorf("empty list should carry the no-stored-runs note: %+v", out)
	}
}
