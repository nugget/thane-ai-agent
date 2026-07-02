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
	// Signed tiered deltas per the model-facing convention: 4h12m ago
	// renders hours+minutes, last wake 47s ago stays exact seconds.
	if v.StartedDelta == nil || *v.StartedDelta != "-4h12m" {
		t.Errorf("StartedDelta = %v, want -4h12m", v.StartedDelta)
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
	if sleeping.NextWakeDelta == nil || *sleeping.NextWakeDelta != "+1h39m" {
		t.Errorf("NextWakeDelta = %v, want +1h39m", sleeping.NextWakeDelta)
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
	if processing.NextWakeDelta != nil || processing.CurrentSleepDuration != nil {
		t.Errorf("non-sleeping loop should have nil next_wake/current_sleep, got %v/%v",
			processing.NextWakeDelta, processing.CurrentSleepDuration)
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
		Config: Config{Operation: OperationContainer, Intent: "away trips"},
	})

	if container.ChildCount != 2 {
		t.Errorf("ChildCount = %d, want 2", container.ChildCount)
	}
	if container.Intent != "away trips" {
		t.Errorf("Intent = %q, want the resolved Config.Intent surfaced", container.Intent)
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

func TestDefinitionViewResolver_FromDefinition_StoredNotRunning(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	created := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	snaps := []DefinitionSnapshot{
		{
			Name: "reservoir", Source: DefinitionSourceOverlay,
			PolicyState: "active", PolicySource: "overlay",
			Spec: Spec{
				Name: "reservoir", Operation: OperationService, Task: "watch the reservoir",
				Intent: "Keep a current read on the reservoir level", ParentName: "watchers",
				Origin: &OriginInfo{RequestID: "r_42", ConversationID: "c1", CreatedByLoopID: "lp_sig", CreatedAt: created},
			},
		},
		{Name: "watchers", Source: DefinitionSourceConfig, Spec: Spec{Name: "watchers", Operation: OperationContainer}},
	}
	r := NewDefinitionViewResolver(snaps, now)

	v := r.FromDefinition(snaps[0], DefinitionEligibilityStatus{Eligible: true}, nil)

	if v.Running {
		t.Error("Running should be false for a stored-not-running definition")
	}
	// Live-only pointers stay nil — "not running", never a misleading zero.
	if v.ID != nil || v.State != nil || v.Iterations != nil || v.ConsecutiveErrors != nil || v.StartedDelta != nil {
		t.Errorf("live-only fields must be nil when not running: id=%v state=%v iters=%v consec=%v started=%v",
			v.ID, v.State, v.Iterations, v.ConsecutiveErrors, v.StartedDelta)
	}
	// effective_* are nil (not evaluated), distinct from [] (running-and-empty).
	if v.EffectiveTags != nil {
		t.Errorf("EffectiveTags = %#v, want nil for a stored (non-running) definition", v.EffectiveTags)
	}
	// Static fields come from the Spec.
	if v.Operation != "service" || v.Task != "watch the reservoir" ||
		v.Intent != "Keep a current read on the reservoir level" {
		t.Errorf("static fields not surfaced from Spec: op=%q task=%q intent=%q", v.Operation, v.Task, v.Intent)
	}
	// Origin (C2) surfaced from the Spec — the payoff of B1.
	if v.Origin == nil || v.Origin.RequestID != "r_42" || !v.Origin.CreatedAt.Equal(created) {
		t.Errorf("Origin not surfaced from Spec: %+v", v.Origin)
	}
	// Graph resolved from parent_name, not live loop IDs.
	if v.ParentName == nil || *v.ParentName != "watchers" {
		t.Errorf("ParentName = %v, want watchers (from Spec.ParentName)", v.ParentName)
	}
	if len(v.Ancestry) != 1 || v.Ancestry[0] != "watchers" {
		t.Errorf("Ancestry = %v, want [watchers]", v.Ancestry)
	}
	// Policy comes from the stored snapshot (a definition carries its own).
	if v.PolicyState != "active" || v.PolicySource != "overlay" || !v.Eligible {
		t.Errorf("policy from snapshot: state=%q source=%q eligible=%v", v.PolicyState, v.PolicySource, v.Eligible)
	}
}

func TestDefinitionViewResolver_FromDefinition_RunningOverlaysLive(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	snaps := []DefinitionSnapshot{
		{
			Name: "reservoir", Source: DefinitionSourceOverlay, PolicyState: "active",
			Spec: Spec{Name: "reservoir", Operation: OperationService, Task: "watch", Intent: "watch the water"},
		},
	}
	r := NewDefinitionViewResolver(snaps, now)

	live := Status{
		ID: "lp_res", Name: "reservoir", State: State("sleeping"),
		StartedAt: now.Add(-2 * time.Hour), Iterations: 50, Attempts: 51,
		TotalInputTokens: 1000, ContextWindow: 200000, LastInputTokens: 20000,
		EffectiveTags: []EffectiveTag{{Tag: "water", From: "self"}},
		Config:        Config{Operation: OperationService},
	}
	v := r.FromDefinition(snaps[0], DefinitionEligibilityStatus{Eligible: true}, &live)

	if !v.Running {
		t.Error("Running should be true when a backing loop is present")
	}
	// Live-only fields overlaid from the Status, identical to a FromStatus row.
	if v.ID == nil || *v.ID != "lp_res" {
		t.Errorf("ID = %v, want lp_res from the live status", v.ID)
	}
	if v.Iterations == nil || *v.Iterations != 50 {
		t.Errorf("Iterations = %v, want 50 from the live status", v.Iterations)
	}
	if v.ContextFillPct == nil || *v.ContextFillPct != 10 { // 20000*100/200000
		t.Errorf("ContextFillPct = %v, want 10", v.ContextFillPct)
	}
	if v.StartedDelta == nil || *v.StartedDelta != "-2h" {
		t.Errorf("StartedDelta = %v, want -2h", v.StartedDelta)
	}
	if len(v.EffectiveTags) != 1 || v.EffectiveTags[0].Tag != "water" {
		t.Errorf("EffectiveTags = %#v, want the live inheritance overlaid", v.EffectiveTags)
	}
	// Static fields still come from the Spec, not the (sparser) live Config.
	if v.Intent != "watch the water" {
		t.Errorf("Intent = %q, want the Spec intent even when running", v.Intent)
	}
}

func TestDefinitionViewResolver_FromDefinition_GraphFromNames(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	snaps := []DefinitionSnapshot{
		{Name: "watchers", Spec: Spec{Name: "watchers", Operation: OperationContainer}},
		{Name: "a", Spec: Spec{Name: "a", Operation: OperationService, ParentName: "watchers"}},
		{Name: "b", Spec: Spec{Name: "b", Operation: OperationService, ParentName: "watchers"}},
	}
	r := NewDefinitionViewResolver(snaps, now)

	container := r.FromDefinition(snaps[0], DefinitionEligibilityStatus{Eligible: true}, nil)
	if container.ChildCount != 2 {
		t.Errorf("ChildCount = %d, want 2 (counted from the parent_name graph)", container.ChildCount)
	}
	if container.ParentName != nil || len(container.Ancestry) != 0 {
		t.Errorf("top-level container should have nil parent / empty ancestry, got %v / %v",
			container.ParentName, container.Ancestry)
	}
}

// TestLoopView_OperationNormalizedConsistently guards the two projectors against
// disagreeing on a loop's operation: an unset operation is valid and must
// normalize to the canonical "request_reply" on BOTH FromStatus and
// FromDefinition, never "" on one and "request_reply" on the other.
func TestLoopView_OperationNormalizedConsistently(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	fromStatus := NewLoopViewResolver(nil, nil, now).FromStatus(Status{
		ID: "lp_x", Name: "adhoc", State: State("processing"),
		Config: Config{Task: "do a thing"}, // Operation unset
	})
	snaps := []DefinitionSnapshot{{Name: "adhoc", Spec: Spec{Name: "adhoc", Task: "do a thing"}}}
	fromDef := NewDefinitionViewResolver(snaps, now).FromDefinition(snaps[0], DefinitionEligibilityStatus{}, nil)

	if fromStatus.Operation != "request_reply" || fromDef.Operation != "request_reply" {
		t.Errorf("unset operation must normalize to request_reply on both projectors: FromStatus=%q FromDefinition=%q",
			fromStatus.Operation, fromDef.Operation)
	}
}

// TestFromDefinition_RunningTakesLiveEventDrivenAndState guards that a running
// definition-backed row reflects live runtime values — EventDriven from the
// Status (not re-derived from the spec operation) and an empty state normalized
// to "running" — so it stays byte-identical to a FromStatus row.
func TestFromDefinition_RunningTakesLiveEventDrivenAndState(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	snaps := []DefinitionSnapshot{{Name: "x", Spec: Spec{Name: "x", Operation: OperationService}}}
	r := NewDefinitionViewResolver(snaps, now)

	// Live loop reports event_driven and an empty state.
	live := Status{ID: "lp_x", Name: "x", State: State(""), EventDriven: true, Config: Config{Operation: OperationService}}
	v := r.FromDefinition(snaps[0], DefinitionEligibilityStatus{}, &live)
	if !v.EventDriven {
		t.Error("a running row must take EventDriven from the live Status, not the spec operation")
	}
	if v.State == nil || *v.State != "running" {
		t.Errorf("empty live state must normalize to \"running\", got %v", v.State)
	}

	// The stored (non-running) counterpart keeps the spec-derived value.
	stored := r.FromDefinition(snaps[0], DefinitionEligibilityStatus{}, nil)
	if stored.EventDriven {
		t.Error("a stored (non-running) service definition must not report event_driven")
	}
	if stored.State != nil {
		t.Errorf("a stored definition must have nil state, got %v", stored.State)
	}
}
