package loop

import (
	"strings"
	"time"
)

const definitionRuntimeStateNotRunning = "not_running"

// DefinitionRuntimeStatus summarizes the live runtime state currently
// associated with one stored loop definition.
type DefinitionRuntimeStatus struct {
	// Running reports whether a live loop instance currently exists for
	// this definition.
	Running bool `yaml:"running" json:"running"`
	// LoopID is the backing live loop instance, when one is present.
	LoopID string `yaml:"loop_id,omitempty" json:"loop_id,omitempty"`
	// State is the current runtime lifecycle state of the backing loop.
	State State `yaml:"state,omitempty" json:"state,omitempty"`
	// StartedAt is when the current backing loop instance started.
	StartedAt time.Time `yaml:"started_at,omitempty" json:"started_at,omitempty"`
	// LastWakeAt is when the current backing loop most recently began an
	// iteration.
	LastWakeAt time.Time `yaml:"last_wake_at,omitempty" json:"last_wake_at,omitempty"`
	// Iterations is the number of successful iterations completed by the
	// current backing loop instance.
	Iterations int `yaml:"iterations,omitempty" json:"iterations,omitempty"`
	// Attempts is the number of total iteration attempts completed by the
	// current backing loop instance.
	Attempts int `yaml:"attempts,omitempty" json:"attempts,omitempty"`
	// LastError is the most recent runtime error from the current backing
	// loop instance.
	LastError string `yaml:"last_error,omitempty" json:"last_error,omitempty"`
}

// DefinitionView is the combined stored-definition and live-runtime view
// exposed by loop read surfaces.
type DefinitionView struct {
	DefinitionSnapshot `yaml:",inline"`
	Eligibility        DefinitionEligibilityStatus `yaml:"eligibility,omitempty" json:"eligibility"`
	Runtime            DefinitionRuntimeStatus     `yaml:"runtime,omitempty" json:"runtime"`
	Warnings           []DefinitionWarning         `yaml:"warnings,omitempty" json:"warnings,omitempty"`
	// EffectiveConditions lists each ancestor's Conditions
	// evaluation with provenance. Surfaces the cascade behind
	// [Eligibility]: a leaf reading [Eligibility].Eligible=false
	// can see *which* ancestor blocked it without re-walking the
	// graph. Empty when the leaf has no container ancestors —
	// [Eligibility] already carries the single-level result.
	EffectiveConditions []EffectiveConditionEvaluation `yaml:"effective_conditions,omitempty" json:"effective_conditions,omitempty"`
	// Effective surfaces the post-ancestor-merge state for inheritable
	// loop fields. Nil when there is nothing meaningful to report —
	// the loop isn't running, the view was built without a live
	// registry (e.g. CLI snapshots), or the loop is running but has
	// no effective tags and no effective subscriptions. Readers should
	// not treat nil as a definitive "not running"; pair it with
	// [DefinitionRuntimeStatus.Running] when that distinction matters.
	// Sits alongside Spec rather than inside Runtime so the
	// declared-vs-effective contrast is at the top of the view and
	// extends naturally as more fields become inheritable.
	Effective *DefinitionEffectiveState `yaml:"effective,omitempty" json:"effective,omitempty"`
}

// DefinitionEffectiveState is the post-ancestor-merge snapshot of
// inheritable fields for one definition's live loop. Each entry
// carries provenance so the reader knows which values are this
// loop's own and which came from a container ancestor.
type DefinitionEffectiveState struct {
	// Tags is the deduplicated union of the loop's own Spec.Tags and
	// every container ancestor's tags, ordered own-first.
	Tags []EffectiveTag `yaml:"tags,omitempty" json:"tags,omitempty"`
	// Subscriptions is the deduplicated union of the loop's own
	// Spec.Subscriptions and every container ancestor's, first-wins
	// on entity_id (own declarations override inherited options).
	Subscriptions []EffectiveSubscription `yaml:"subscriptions,omitempty" json:"subscriptions,omitempty"`
	// ExcludeTools is the union of the loop's own ExcludeTools and
	// every container ancestor's. Union semantics — a descendant
	// cannot un-exclude an ancestor's restriction.
	ExcludeTools []EffectiveExcludeTool `yaml:"exclude_tools,omitempty" json:"exclude_tools,omitempty"`
	// RoutingFactors is the merged routing-factor map, child-wins on
	// key collision. Each entry names its origin loop.
	RoutingFactors []EffectiveRoutingFactor `yaml:"routing_factors,omitempty" json:"routing_factors,omitempty"`
	// DelegationGating is the resolved gating value plus its origin.
	// Nil when no loop in the chain declared a non-empty value (the
	// agent default applies).
	DelegationGating *EffectiveDelegationGating `yaml:"delegation_gating,omitempty" json:"delegation_gating,omitempty"`
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

// DefinitionViewOption tunes [BuildDefinitionRegistryView]. Used today
// to opt into effective-state population by passing a live registry
// via [WithLiveRegistry]; older callers that don't supply one continue
// to get a view without the Effective field, which is the right shape
// for surfaces that don't have access to a running loop graph.
type DefinitionViewOption func(*definitionViewOptions)

type definitionViewOptions struct {
	loops *Registry
}

// WithLiveRegistry attaches a live loop registry to the view build so
// each definition's running loop can be inspected for inherited tags
// and subscriptions. Without it, [DefinitionView.Effective] stays nil
// — a safe default for snapshots taken outside the running app (e.g.
// CLI inspection tools or the loop_definition_lint surface).
func WithLiveRegistry(loops *Registry) DefinitionViewOption {
	return func(o *definitionViewOptions) {
		o.loops = loops
	}
}

// BuildDefinitionRegistryView combines the durable definition snapshot
// with an optional runtime-state map to produce the effective loop
// registry view used by API and tool read surfaces.
func BuildDefinitionRegistryView(snapshot *DefinitionRegistrySnapshot, runtime map[string]DefinitionRuntimeStatus, opts ...DefinitionViewOption) *DefinitionRegistryView {
	return buildDefinitionRegistryViewAt(snapshot, runtime, time.Now(), opts...)
}

func buildDefinitionRegistryViewAt(snapshot *DefinitionRegistrySnapshot, runtime map[string]DefinitionRuntimeStatus, now time.Time, opts ...DefinitionViewOption) *DefinitionRegistryView {
	if snapshot == nil {
		return nil
	}
	options := definitionViewOptions{}
	for _, opt := range opts {
		opt(&options)
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

	// Index the snapshot by name once so the eligibility cascade
	// walks parent_name through the same snapshot we're rendering —
	// avoids both per-definition registry lock acquisition and the
	// risk of observing a different definition state between
	// snapshot and ancestor lookup.
	byName := make(map[string]Spec, len(snapshot.Definitions))
	for _, def := range snapshot.Definitions {
		byName[def.Name] = def.Spec
	}

	for _, def := range snapshot.Definitions {
		chain := ancestorChainFromSnapshot(byName, def.Name)
		eligibility, conditionEvals := EvaluateEffectiveConditions(chain, now)
		warnings := BuildDefinitionWarnings(def.Spec)
		status, ok := runtime[def.Name]
		if ok && status.Running {
			view.RunningDefinitions++
			state := string(status.State)
			if state == "" {
				state = "running"
			}
			view.ByRuntimeState[state]++
		} else {
			view.ByRuntimeState[definitionRuntimeStateNotRunning]++
			status = DefinitionRuntimeStatus{}
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
		dv := DefinitionView{
			DefinitionSnapshot: def,
			Eligibility:        eligibility,
			Runtime:            status,
			Warnings:           warnings,
		}
		// Surface the per-ancestor evaluations only when the cascade
		// actually went through more than one level — for a leaf with
		// no container parent, the single self-evaluation is already
		// what [Eligibility] reports and adding a single-entry list
		// would be noise.
		if len(conditionEvals) > 1 {
			dv.EffectiveConditions = conditionEvals
		}
		if options.loops != nil && status.Running && status.LoopID != "" {
			// One walk per definition: per-field Effective* calls
			// would do separate ancestor traversals that could observe
			// different snapshots if SetSubscriptions ran between them.
			eff := options.loops.effectiveState(status.LoopID)
			if len(eff.Tags) > 0 || len(eff.Subscriptions) > 0 ||
				len(eff.ExcludeTools) > 0 || len(eff.RoutingFactors) > 0 ||
				eff.DelegationGating != nil {
				dv.Effective = &DefinitionEffectiveState{
					Tags:             eff.Tags,
					Subscriptions:    eff.Subscriptions,
					ExcludeTools:     eff.ExcludeTools,
					RoutingFactors:   eff.RoutingFactors,
					DelegationGating: eff.DelegationGating,
				}
			}
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
