package loop

import (
	"strings"
	"time"
)

const definitionRuntimeStateNotRunning = "not_running"

// DefinitionView is the combined stored-definition and live-runtime view
// exposed by loop read surfaces. The canonical loop projection lives in Loop —
// a [LoopView] built via [DefinitionViewResolver.FromDefinition], with the
// live half overlaid when the definition has a running backing loop. The
// remaining fields are definition-corpus framing a live LoopView intentionally
// does not carry: the raw authored Spec (for round-trip editing), the
// provenance source, lint warnings, and the per-ancestor eligibility cascade.
type DefinitionView struct {
	// Name is the definition's stable identifier (also [LoopView.Name]).
	Name string `yaml:"name,omitempty" json:"name"`
	// Source records whether the definition came from config or the overlay.
	Source DefinitionSource `yaml:"source,omitempty" json:"source"`
	// UpdatedAt is when the stored definition was last written.
	UpdatedAt time.Time `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
	// Spec is the raw authored definition, kept for round-trip editing
	// (loop_definition_update reads it before re-committing). Loop is the
	// resolved canonical view of the same definition.
	Spec Spec `yaml:"spec,omitempty" json:"spec"`
	// Loop is the canonical "ps auxwwww" projection of this definition:
	// stored-static fields always, plus the live-only half (state, economics,
	// errors, effective_* inheritance) overlaid when a backing loop is running.
	// [LoopView.Running] discriminates. Never nil.
	Loop *LoopView `yaml:"loop,omitempty" json:"loop"`
	// Eligibility is the detailed policy/condition eligibility result. Loop
	// carries the eligible bool and policy_state; this carries the reason and
	// the blocking detail.
	Eligibility DefinitionEligibilityStatus `yaml:"eligibility,omitempty" json:"eligibility"`
	// Warnings are lint findings for the stored spec.
	Warnings []DefinitionWarning `yaml:"warnings,omitempty" json:"warnings,omitempty"`
	// EffectiveConditions lists each ancestor's Conditions evaluation with
	// provenance. Surfaces the cascade behind [Eligibility]: a leaf reading
	// Eligible=false can see *which* ancestor blocked it without re-walking the
	// graph. Empty when the leaf has no container ancestors — [Eligibility]
	// already carries the single-level result.
	EffectiveConditions []EffectiveConditionEvaluation `yaml:"effective_conditions,omitempty" json:"effective_conditions,omitempty"`
}

// DefinitionRegistryView is the effective combined view of stored loop
// definitions plus their current live runtime state.
type DefinitionRegistryView struct {
	Generation              int64            `yaml:"generation,omitempty" json:"generation"`
	UpdatedAt               time.Time        `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
	ConfigDefinitions       int              `yaml:"config_definitions,omitempty" json:"config_definitions"`
	OverlayDefinitions      int              `yaml:"overlay_definitions,omitempty" json:"overlay_definitions"`
	RunningDefinitions      int              `yaml:"running_definitions,omitempty" json:"running_definitions"`
	DefinitionsWithWarnings int              `yaml:"definitions_with_warnings,omitempty" json:"definitions_with_warnings,omitempty"`
	WarningCount            int              `yaml:"warning_count,omitempty" json:"warning_count,omitempty"`
	ByPolicyState           map[string]int   `yaml:"by_policy_state,omitempty" json:"by_policy_state,omitempty"`
	ByEligibilityState      map[string]int   `yaml:"by_eligibility_state,omitempty" json:"by_eligibility_state,omitempty"`
	ByRuntimeState          map[string]int   `yaml:"by_runtime_state,omitempty" json:"by_runtime_state,omitempty"`
	Definitions             []DefinitionView `yaml:"definitions,omitempty" json:"definitions,omitempty"`
}

// BuildDefinitionRegistryView combines the durable definition snapshot with an
// optional map of live loop Statuses — keyed by definition name, where presence
// means a backing loop is running — to produce the effective loop registry view
// used by API and tool read surfaces. Each definition is projected into a
// canonical [LoopView] via [DefinitionViewResolver.FromDefinition], with the
// live half overlaid from its Status (which already carries the resolved
// effective_* inheritance lists). Pass nil for liveByName on surfaces with no
// running loop graph (e.g. CLI snapshots, loop_definition_lint); those views
// project every definition as stored-only.
func BuildDefinitionRegistryView(snapshot *DefinitionRegistrySnapshot, liveByName map[string]Status) *DefinitionRegistryView {
	return buildDefinitionRegistryViewAt(snapshot, liveByName, time.Now())
}

func buildDefinitionRegistryViewAt(snapshot *DefinitionRegistrySnapshot, liveByName map[string]Status, now time.Time) *DefinitionRegistryView {
	if snapshot == nil {
		return nil
	}

	view := &DefinitionRegistryView{
		Generation:         snapshot.Generation,
		UpdatedAt:          snapshot.UpdatedAt,
		ConfigDefinitions:  snapshot.ConfigDefinitions,
		OverlayDefinitions: snapshot.OverlayDefinitions,
		ByPolicyState:      make(map[string]int),
		ByEligibilityState: make(map[string]int),
		ByRuntimeState:     make(map[string]int),
		Definitions:        make([]DefinitionView, 0, len(snapshot.Definitions)),
	}

	// Index the snapshot by name once so the eligibility cascade walks
	// parent_name through the same snapshot we're rendering — avoids both
	// per-definition registry lock acquisition and the risk of observing a
	// different definition state between snapshot and ancestor lookup.
	byName := make(map[string]Spec, len(snapshot.Definitions))
	for _, def := range snapshot.Definitions {
		byName[def.Name] = def.Spec
	}
	resolver := NewDefinitionViewResolver(snapshot.Definitions, now)

	for _, def := range snapshot.Definitions {
		chain := ancestorChainFromSnapshot(byName, def.Name)
		eligibility, conditionEvals := EvaluateEffectiveConditions(chain, now)
		warnings := BuildDefinitionWarnings(def.Spec)

		var live *Status
		if s, ok := liveByName[def.Name]; ok {
			sCopy := s
			live = &sCopy
			view.RunningDefinitions++
			state := string(s.State)
			if state == "" {
				state = "running"
			}
			view.ByRuntimeState[state]++
		} else {
			view.ByRuntimeState[definitionRuntimeStateNotRunning]++
		}

		view.ByPolicyState[string(def.PolicyState)]++
		if eligibility.Eligible {
			view.ByEligibilityState[definitionEligibilityStateEligible]++
		} else {
			view.ByEligibilityState[definitionEligibilityStateIneligible]++
		}
		if len(warnings) > 0 {
			view.DefinitionsWithWarnings++
			view.WarningCount += len(warnings)
		}

		loop := resolver.FromDefinition(def, eligibility, live)
		dv := DefinitionView{
			Name:        def.Name,
			Source:      def.Source,
			UpdatedAt:   def.UpdatedAt,
			Spec:        def.Spec,
			Loop:        &loop,
			Eligibility: eligibility,
			Warnings:    warnings,
		}
		// Surface the per-ancestor evaluations only when the cascade actually
		// went through more than one level — for a leaf with no container
		// parent, the single self-evaluation is already what [Eligibility]
		// reports and a single-entry list would be noise.
		if len(conditionEvals) > 1 {
			dv.EffectiveConditions = conditionEvals
		}
		view.Definitions = append(view.Definitions, dv)
	}

	return view
}

// ancestorChainFromSnapshot walks parent_name through a name-indexed
// snapshot map and returns the chain in parent-first walk order
// (index 0 is the leaf). Pure function over the snapshot — no
// registry lock required — so the view builder can compute one
// chain per definition without re-acquiring locks. Bounded by
// [definitionAncestorWalkLimit] like [DefinitionRegistry.AncestorSpecs].
func ancestorChainFromSnapshot(byName map[string]Spec, name string) []Spec {
	spec, ok := byName[name]
	if !ok {
		return nil
	}
	chain := []Spec{spec}
	seen := map[string]struct{}{name: {}}
	current := spec
	for i := 0; i < definitionAncestorWalkLimit; i++ {
		parentName := strings.TrimSpace(current.ParentName)
		if parentName == "" {
			break
		}
		if _, dup := seen[parentName]; dup {
			break
		}
		parent, ok := byName[parentName]
		if !ok {
			break
		}
		chain = append(chain, parent)
		seen[parentName] = struct{}{}
		current = parent
	}
	return chain
}
