package loop

import "time"

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
// exposed by loops-ng read surfaces.
type DefinitionView struct {
	DefinitionSnapshot `yaml:",inline"`
	Runtime            DefinitionRuntimeStatus `yaml:"runtime,omitempty" json:"runtime"`
}

// DefinitionRegistryView is the effective combined view of stored loop
// definitions plus their current live runtime state.
type DefinitionRegistryView struct {
	Generation         int64            `yaml:"generation,omitempty" json:"generation"`
	UpdatedAt          time.Time        `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
	ConfigDefinitions  int              `yaml:"config_definitions,omitempty" json:"config_definitions"`
	OverlayDefinitions int              `yaml:"overlay_definitions,omitempty" json:"overlay_definitions"`
	RunningDefinitions int              `yaml:"running_definitions,omitempty" json:"running_definitions"`
	ByPolicyState      map[string]int   `yaml:"by_policy_state,omitempty" json:"by_policy_state,omitempty"`
	ByRuntimeState     map[string]int   `yaml:"by_runtime_state,omitempty" json:"by_runtime_state,omitempty"`
	Definitions        []DefinitionView `yaml:"definitions,omitempty" json:"definitions,omitempty"`
}

// BuildDefinitionRegistryView combines the durable definition snapshot
// with an optional runtime-state map to produce the effective loops-ng
// registry view used by API and tool read surfaces.
func BuildDefinitionRegistryView(snapshot *DefinitionRegistrySnapshot, runtime map[string]DefinitionRuntimeStatus) *DefinitionRegistryView {
	if snapshot == nil {
		return nil
	}

	view := &DefinitionRegistryView{
		Generation:         snapshot.Generation,
		UpdatedAt:          snapshot.UpdatedAt,
		ConfigDefinitions:  snapshot.ConfigDefinitions,
		OverlayDefinitions: snapshot.OverlayDefinitions,
		ByPolicyState:      make(map[string]int),
		ByRuntimeState:     make(map[string]int),
		Definitions:        make([]DefinitionView, 0, len(snapshot.Definitions)),
	}

	for _, def := range snapshot.Definitions {
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
		view.Definitions = append(view.Definitions, DefinitionView{
			DefinitionSnapshot: def,
			Runtime:            status,
		})
	}

	return view
}
