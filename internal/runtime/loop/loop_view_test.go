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
	// Signed exact-second deltas per the model-facing convention: 4h12m =
	// 15120s ago, last wake 47s ago.
	if v.StartedDelta == nil || *v.StartedDelta != "-15120s" {
		t.Errorf("StartedDelta = %v, want -15120s", v.StartedDelta)
	}
	if v.LastWakeDelta == nil || *v.LastWakeDelta != "-47s" {
		t.Errorf("LastWakeDelta = %v, want -47s", v.LastWakeDelta)
	}
}

func TestLoopViewResolver_FromStatus_NextWake(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	r := NewLoopViewResolver(nil, nil, now)

	// A timer-sleeping loop reports the scheduled wake (signed-second delta)
	// and the self-paced interval it is honoring.
	sleeping := r.FromStatus(Status{
		ID:           "lp_a",
		Name:         "a",
		State:        State("sleeping"),
		SleepUntil:   now.Add(99 * time.Minute),
		CurrentSleep: 99 * time.Minute,
		Config:       Config{Operation: OperationService},
	})
	if sleeping.NextWakeIn == nil || *sleeping.NextWakeIn != "+5940s" {
		t.Errorf("NextWakeIn = %v, want +5940s", sleeping.NextWakeIn)
	}
	// Unsigned seconds (a duration magnitude), not a delta: 99m = 5940s.
	if sleeping.CurrentSleepDuration == nil || *sleeping.CurrentSleepDuration != "5940s" {
		t.Errorf("CurrentSleepDuration = %v, want 5940s", sleeping.CurrentSleepDuration)
	}

	// A loop not in a timer sleep (no SleepUntil) reports null for both —
	// processing loops and event-driven loops have no scheduled wake.
	processing := r.FromStatus(Status{
		ID:     "lp_b",
		Name:   "b",
		State:  State("processing"),
		Config: Config{Operation: OperationService},
	})
	if processing.NextWakeIn != nil || processing.CurrentSleepDuration != nil {
		t.Errorf("non-sleeping loop should have nil next_wake/current_sleep, got %v/%v",
			processing.NextWakeIn, processing.CurrentSleepDuration)
	}
}

func TestLoopViewResolver_FromStatus_HandlerOnlyAndCleanError(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	r := NewLoopViewResolver(nil, nil, now)

	v := r.FromStatus(Status{
		ID:               "lp_owu",
		Name:             "owu",
		State:            State("waiting"),
		HandlerOnly:      true,
		Iterations:       537,
		Attempts:         537,
		TotalInputTokens: 0,
		ContextWindow:    200000,
		LastError:        "",
		Config:           Config{Operation: OperationService},
	})

	// Handler-only loops run no LLM iterations — token fields are nil, not 0,
	// so "0" can't read as a real datum.
	if v.TotalInputTokens != nil || v.ContextWindow != nil || v.ContextFillPct != nil {
		t.Errorf("handler-only token fields should be nil, got in=%v ctx=%v fill=%v",
			v.TotalInputTokens, v.ContextWindow, v.ContextFillPct)
	}
	// Iteration counters are still meaningful for handler loops.
	if v.Iterations == nil || *v.Iterations != 537 {
		t.Errorf("Iterations = %v, want 537 even for handler-only", v.Iterations)
	}
	// No error => null, not "".
	if v.LastError != nil {
		t.Errorf("LastError = %v, want nil for a clean loop", v.LastError)
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
