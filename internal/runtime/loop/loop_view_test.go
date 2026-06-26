package loop

import (
	"testing"
	"time"
)

func TestLoopViewResolver_FromStatus_RunningService(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	statuses := []Status{
		{ID: "lp_core", Name: "core", Config: Config{Operation: OperationContainer}},
		{ID: "lp_travel", Name: "travel", ParentID: "lp_core", Config: Config{Operation: OperationContainer}},
		{ID: "lp_trip", Name: "trip", ParentID: "lp_travel", Config: Config{Operation: OperationService}},
	}
	policy := map[string]LoopPolicyInfo{
		"trip": {State: "active", Source: "overlay", Eligible: true, HasPolicy: true},
	}
	r := NewLoopViewResolver(statuses, policy, now)

	trip := Status{
		ID:                    "lp_trip",
		Name:                  "trip",
		ParentID:              "lp_travel",
		State:                 State("sleeping"),
		StartedAt:             now.Add(-(4*time.Hour + 12*time.Minute)),
		LastWakeAt:            now.Add(-47 * time.Second),
		Iterations:            138,
		Attempts:              141,
		TotalInputTokens:      2104882,
		TotalOutputTokens:     51440,
		LastInputTokens:       18000,
		ContextWindow:         200000,
		ConsecutiveErrors:     0,
		LastError:             "",
		LastSupervisorIter:    135,
		LastSupervisorTrigger: SupervisorTrigger("random"),
		EffectiveTags:         []EffectiveTag{{Tag: "home", From: "travel"}, {Tag: "curate", From: "self"}},
		Config:                Config{Operation: OperationService, Task: "watch ranch", Supervisor: true, SupervisorProb: 0.15},
	}
	v := r.FromStatus(trip)

	if !v.Running {
		t.Error("Running should be true for a live loop")
	}
	if v.ID == nil || *v.ID != "lp_trip" {
		t.Errorf("ID = %v, want lp_trip", v.ID)
	}
	if v.ParentName == nil || *v.ParentName != "travel" {
		t.Errorf("ParentName = %v, want travel", v.ParentName)
	}
	if got := v.Ancestry; len(got) != 2 || got[0] != "core" || got[1] != "travel" {
		t.Errorf("Ancestry = %v, want [core travel] (root→leaf)", got)
	}
	// iterations = SUCCESSFUL turns, attempts = total incl failures.
	if v.Iterations == nil || *v.Iterations != 138 {
		t.Errorf("Iterations = %v, want 138", v.Iterations)
	}
	if v.Attempts == nil || *v.Attempts != 141 {
		t.Errorf("Attempts = %v, want 141", v.Attempts)
	}
	// Precomputed so the model never divides: 18000*100/200000 = 9.
	if v.ContextFillPct == nil || *v.ContextFillPct != 9 {
		t.Errorf("ContextFillPct = %v, want 9", v.ContextFillPct)
	}
	// 138 - 135 = 3 successful turns since the last supervisor pass.
	if v.SupervisorItersAgo == nil || *v.SupervisorItersAgo != 3 {
		t.Errorf("SupervisorItersAgo = %v, want 3", v.SupervisorItersAgo)
	}
	if v.LastSupervisorTrigger == nil || *v.LastSupervisorTrigger != "random" {
		t.Errorf("LastSupervisorTrigger = %v, want random", v.LastSupervisorTrigger)
	}
	if v.PolicyState != "active" || !v.Eligible {
		t.Errorf("policy join: state=%q eligible=%v, want active/true", v.PolicyState, v.Eligible)
	}
	if len(v.EffectiveTags) != 2 || v.EffectiveTags[0].From != "travel" {
		t.Errorf("EffectiveTags = %#v, want inheritance provenance carried through", v.EffectiveTags)
	}
	if v.StartedDelta == nil || *v.StartedDelta != "running for 4h12m" {
		t.Errorf("StartedDelta = %v, want 'running for 4h12m'", v.StartedDelta)
	}
	if v.LastWakeDelta == nil || *v.LastWakeDelta != "47s ago" {
		t.Errorf("LastWakeDelta = %v, want '47s ago'", v.LastWakeDelta)
	}
}

func TestLoopViewResolver_FromStatus_ContainerAndEphemeral(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	statuses := []Status{
		{ID: "lp_travel", Name: "travel", Config: Config{Operation: OperationContainer}},
		{ID: "c1", Name: "c1", ParentID: "lp_travel", Config: Config{Operation: OperationService}},
		{ID: "c2", Name: "c2", ParentID: "lp_travel", Config: Config{Operation: OperationService}},
	}
	r := NewLoopViewResolver(statuses, nil, now) // nil policy join

	container := r.FromStatus(Status{
		ID:     "lp_travel",
		Name:   "travel",
		State:  State("pending"),
		Config: Config{Operation: OperationContainer, Metadata: map[string]string{"intent": "away trips"}},
	})

	if container.ChildCount != 2 {
		t.Errorf("ChildCount = %d, want 2", container.ChildCount)
	}
	if container.Intent != "away trips" {
		t.Errorf("Intent = %q, want the metadata intent surfaced", container.Intent)
	}
	if container.ParentName != nil {
		t.Errorf("top-level container ParentName = %v, want nil", container.ParentName)
	}
	if len(container.Ancestry) != 0 {
		t.Errorf("top-level Ancestry = %v, want []", container.Ancestry)
	}
	// No definition policy join wired → explicit "ephemeral", not a misleading default.
	if container.PolicyState != "ephemeral" {
		t.Errorf("PolicyState = %q, want ephemeral when no policy join", container.PolicyState)
	}
	// Running loop with nothing to inherit must serialize [] (evaluated-empty),
	// never null (which would read as "not running").
	if container.EffectiveTags == nil || len(container.EffectiveTags) != 0 {
		t.Errorf("EffectiveTags = %#v, want non-nil empty slice for a running loop", container.EffectiveTags)
	}
}
