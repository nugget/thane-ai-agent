package loop

import (
	"strings"
	"testing"
	"time"
)

// TestEvaluateEffectiveConditionsAllEligible covers the happy path:
// when every ancestor and the leaf are eligible, the aggregate is
// eligible and each per-level evaluation reports the same.
func TestEvaluateEffectiveConditionsAllEligible(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 25, 14, 0, 0, 0, time.UTC) // Monday afternoon
	chain := []Spec{
		{Name: "leaf"}, // no conditions = always eligible
		{Name: "parent"},
	}

	agg, evals := EvaluateEffectiveConditions(chain, now)
	if !agg.Eligible {
		t.Errorf("aggregate Eligible = false, want true: %+v", agg)
	}
	if len(evals) != 2 {
		t.Fatalf("evals len = %d, want 2", len(evals))
	}
	if evals[0].From != EffectiveOriginSelf {
		t.Errorf("evals[0].From = %q, want self", evals[0].From)
	}
	if evals[1].From != "parent" {
		t.Errorf("evals[1].From = %q, want parent", evals[1].From)
	}
}

// TestEvaluateEffectiveConditionsAncestorBlocks is the load-bearing
// test for PR-C2: a leaf with no conditions still becomes ineligible
// when an ancestor container's schedule is outside its window. The
// reason names the blocking ancestor.
func TestEvaluateEffectiveConditionsAncestorBlocks(t *testing.T) {
	t.Parallel()

	// Set "now" to a Sunday so the work_hours window (mon-fri) is
	// outside. Use UTC for deterministic comparison.
	now := time.Date(2026, 5, 24, 14, 0, 0, 0, time.UTC) // Sunday
	chain := []Spec{
		{Name: "leaf"},
		{
			Name: "work_hours",
			Conditions: Conditions{
				Schedule: &ScheduleCondition{
					Timezone: "UTC",
					Windows: []ScheduleWindow{
						{Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "09:00", End: "17:00"},
					},
				},
			},
		},
	}

	agg, evals := EvaluateEffectiveConditions(chain, now)
	if agg.Eligible {
		t.Errorf("aggregate Eligible = true, want false (ancestor blocks)")
	}
	if !strings.Contains(agg.Reason, "work_hours") {
		t.Errorf("Reason = %q, should name the blocking ancestor work_hours", agg.Reason)
	}
	if len(evals) != 2 {
		t.Fatalf("evals len = %d, want 2", len(evals))
	}
	if !evals[0].Status.Eligible {
		t.Error("leaf's own evaluation should still report eligible")
	}
	if evals[1].Status.Eligible {
		t.Error("work_hours ancestor should report ineligible")
	}
}

// TestEvaluateEffectiveConditionsClosestBlockingWins covers
// attribution when multiple ancestors would block: the closest one
// (parent-first walk) wins the Reason field. Surfaces the most
// actionable bit — fix this level, not the most distant one.
func TestEvaluateEffectiveConditionsClosestBlockingWins(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 24, 14, 0, 0, 0, time.UTC) // Sunday
	weekdays := []string{"mon", "tue", "wed", "thu", "fri"}
	schedule := &ScheduleCondition{
		Timezone: "UTC",
		Windows:  []ScheduleWindow{{Days: weekdays, Start: "09:00", End: "17:00"}},
	}
	chain := []Spec{
		{Name: "leaf"},
		{Name: "near_block", Conditions: Conditions{Schedule: schedule}},
		{Name: "far_block", Conditions: Conditions{Schedule: schedule}},
	}

	agg, _ := EvaluateEffectiveConditions(chain, now)
	if agg.Eligible {
		t.Fatal("aggregate should be ineligible")
	}
	if !strings.Contains(agg.Reason, "near_block") {
		t.Errorf("Reason = %q, should name the closest blocking ancestor near_block", agg.Reason)
	}
	if strings.Contains(agg.Reason, "far_block") {
		t.Errorf("Reason = %q, should not name the far ancestor — only the closest blocker is actionable", agg.Reason)
	}
}

// TestEvaluateEffectiveConditionsOwnBlockSkipsAttribution covers
// the "self blocks self" path: when the leaf's own conditions are
// ineligible, the reason carries the underlying condition message
// without a misleading "blocked by container" suffix.
func TestEvaluateEffectiveConditionsOwnBlockSkipsAttribution(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 24, 14, 0, 0, 0, time.UTC)
	chain := []Spec{
		{
			Name: "leaf",
			Conditions: Conditions{
				Schedule: &ScheduleCondition{
					Timezone: "UTC",
					Windows: []ScheduleWindow{
						{Days: []string{"mon"}, Start: "09:00", End: "17:00"},
					},
				},
			},
		},
	}

	agg, _ := EvaluateEffectiveConditions(chain, now)
	if agg.Eligible {
		t.Fatal("leaf with off-schedule condition should be ineligible")
	}
	if strings.Contains(agg.Reason, "blocked by container") {
		t.Errorf("Reason = %q, should not blame a container when self blocks", agg.Reason)
	}
}

// TestEvaluateEffectiveConditionsNextTransitionEarliest verifies
// that NextTransitionAt rolls up to the earliest transition across
// the chain. Schedulers need the soonest meaningful tick, not just
// the leaf's own.
func TestEvaluateEffectiveConditionsNextTransitionEarliest(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 25, 14, 0, 0, 0, time.UTC) // Monday afternoon

	// Leaf: window 13:00-23:00 → transitions at 23:00 today.
	// Ancestor: window 10:00-15:00 → transitions at 15:00 today (earlier).
	chain := []Spec{
		{
			Name: "leaf",
			Conditions: Conditions{Schedule: &ScheduleCondition{
				Timezone: "UTC",
				Windows: []ScheduleWindow{
					{Days: []string{"mon"}, Start: "13:00", End: "23:00"},
				},
			}},
		},
		{
			Name: "ancestor",
			Conditions: Conditions{Schedule: &ScheduleCondition{
				Timezone: "UTC",
				Windows: []ScheduleWindow{
					{Days: []string{"mon"}, Start: "10:00", End: "15:00"},
				},
			}},
		},
	}

	agg, _ := EvaluateEffectiveConditions(chain, now)
	if !agg.Eligible {
		t.Fatal("both windows include 14:00 Monday — aggregate should be eligible")
	}
	expected := time.Date(2026, 5, 25, 15, 0, 0, 0, time.UTC)
	if !agg.NextTransitionAt.Equal(expected) {
		t.Errorf("NextTransitionAt = %v, want %v (earliest across chain)", agg.NextTransitionAt, expected)
	}
}

// TestDefinitionRegistryAncestorSpecs covers the walk shape: the
// returned chain is leaf-first, terminates at root, and ignores
// loops in the parent_name graph.
func TestDefinitionRegistryAncestorSpecs(t *testing.T) {
	t.Parallel()

	now := time.Now()
	reg, err := NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	root := Spec{Name: "root", Operation: OperationContainer}
	mid := Spec{Name: "mid", Operation: OperationContainer, ParentName: "root"}
	leaf := Spec{
		Name:         "leaf",
		Task:         "t",
		Operation:    OperationService,
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
		ParentName:   "mid",
	}
	for _, s := range []Spec{root, mid, leaf} {
		if err := reg.Upsert(s, now); err != nil {
			t.Fatalf("upsert %s: %v", s.Name, err)
		}
	}

	chain := reg.AncestorSpecs("leaf")
	if len(chain) != 3 {
		t.Fatalf("chain len = %d, want 3 (leaf, mid, root)", len(chain))
	}
	if chain[0].Name != "leaf" || chain[1].Name != "mid" || chain[2].Name != "root" {
		t.Errorf("walk order = [%s %s %s], want [leaf mid root]", chain[0].Name, chain[1].Name, chain[2].Name)
	}

	if chain := reg.AncestorSpecs("nonexistent"); chain != nil {
		t.Errorf("unknown name returned %v, want nil", chain)
	}
}

// TestDefinitionRegistryEvaluateConditionsCascades verifies the
// convenience wrapper actually walks the chain — a leaf with no
// own conditions becomes ineligible when an ancestor's schedule
// is closed.
func TestDefinitionRegistryEvaluateConditionsCascades(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 24, 14, 0, 0, 0, time.UTC) // Sunday
	reg, err := NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	weekday := &ScheduleCondition{
		Timezone: "UTC",
		Windows: []ScheduleWindow{
			{Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "09:00", End: "17:00"},
		},
	}
	if err := reg.Upsert(Spec{Name: "weekdays", Operation: OperationContainer, Conditions: Conditions{Schedule: weekday}}, now); err != nil {
		t.Fatalf("upsert ancestor: %v", err)
	}
	if err := reg.Upsert(Spec{
		Name:         "leaf",
		Task:         "t",
		Operation:    OperationService,
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
		ParentName:   "weekdays",
	}, now); err != nil {
		t.Fatalf("upsert leaf: %v", err)
	}

	status, evals := reg.EvaluateConditions("leaf", now)
	if status.Eligible {
		t.Errorf("leaf should be ineligible due to ancestor schedule, got %+v", status)
	}
	if !strings.Contains(status.Reason, "weekdays") {
		t.Errorf("Reason = %q, should name the weekdays ancestor", status.Reason)
	}
	if len(evals) != 2 {
		t.Fatalf("evals len = %d, want 2 (leaf + ancestor)", len(evals))
	}
}

// TestBuildDefinitionRegistryViewSurfacesEffectiveConditions
// is the end-to-end test: a leaf blocked by an ancestor's schedule
// surfaces as ineligible on its DefinitionView, with the per-level
// EffectiveConditions slice populated for the cascade.
func TestBuildDefinitionRegistryViewSurfacesEffectiveConditions(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 24, 14, 0, 0, 0, time.UTC) // Sunday
	reg, err := NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	weekday := &ScheduleCondition{
		Timezone: "UTC",
		Windows: []ScheduleWindow{
			{Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "09:00", End: "17:00"},
		},
	}
	if err := reg.Upsert(Spec{Name: "weekdays", Operation: OperationContainer, Conditions: Conditions{Schedule: weekday}}, now); err != nil {
		t.Fatalf("upsert ancestor: %v", err)
	}
	if err := reg.Upsert(Spec{
		Name:         "leaf",
		Task:         "t",
		Operation:    OperationService,
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
		ParentName:   "weekdays",
	}, now); err != nil {
		t.Fatalf("upsert leaf: %v", err)
	}

	view := buildDefinitionRegistryViewAt(reg.Snapshot(), nil, now)
	if view == nil {
		t.Fatal("nil view")
	}

	var leaf *DefinitionView
	for i := range view.Definitions {
		if view.Definitions[i].Name == "leaf" {
			leaf = &view.Definitions[i]
			break
		}
	}
	if leaf == nil {
		t.Fatal("leaf not in view")
	}
	if leaf.Eligibility.Eligible {
		t.Errorf("leaf Eligibility = %+v, want ineligible due to ancestor", leaf.Eligibility)
	}
	if len(leaf.EffectiveConditions) != 2 {
		t.Fatalf("EffectiveConditions len = %d, want 2: %+v", len(leaf.EffectiveConditions), leaf.EffectiveConditions)
	}
	if leaf.EffectiveConditions[0].From != EffectiveOriginSelf {
		t.Errorf("EffectiveConditions[0].From = %q, want self", leaf.EffectiveConditions[0].From)
	}
	if leaf.EffectiveConditions[1].From != "weekdays" {
		t.Errorf("EffectiveConditions[1].From = %q, want weekdays", leaf.EffectiveConditions[1].From)
	}
}

// TestBuildDefinitionRegistryViewOmitsEffectiveConditionsForOrphans
// keeps the view tidy: a definition without container ancestors
// already carries its eligibility in DefinitionView.Eligibility, so
// adding a single-entry EffectiveConditions list would be noise.
func TestBuildDefinitionRegistryViewOmitsEffectiveConditionsForOrphans(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 25, 14, 0, 0, 0, time.UTC)
	reg, err := NewDefinitionRegistry([]Spec{
		{
			Name:         "loner",
			Enabled:      true,
			Task:         "t",
			Operation:    OperationService,
			SleepMin:     time.Minute,
			SleepMax:     time.Minute,
			SleepDefault: time.Minute,
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	view := buildDefinitionRegistryViewAt(reg.Snapshot(), nil, now)
	if view == nil {
		t.Fatal("nil view")
	}
	if got := view.Definitions[0].EffectiveConditions; got != nil {
		t.Errorf("EffectiveConditions = %+v, want nil for single-level definitions", got)
	}
}
