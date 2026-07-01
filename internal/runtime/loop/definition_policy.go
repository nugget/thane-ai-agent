package loop

import (
	"fmt"
	"strings"
	"time"
)

// DefinitionPolicyState describes the effective runtime state of a
// stored loop definition.
type DefinitionPolicyState string

const (
	// DefinitionPolicyStateActive means the definition is eligible for
	// runtime use. Service definitions may auto-start or be launched.
	DefinitionPolicyStateActive DefinitionPolicyState = "active"
	// DefinitionPolicyStatePaused means the definition is temporarily
	// held out of runtime execution. Existing service loops should be
	// stopped, but the definition remains retained for future resume.
	DefinitionPolicyStatePaused DefinitionPolicyState = "paused"
	// DefinitionPolicyStateInactive means the definition is disabled for
	// runtime use. Existing service loops should be stopped.
	DefinitionPolicyStateInactive DefinitionPolicyState = "inactive"
)

// DefinitionPolicySource reports whether the effective runtime state
// comes from the definition's default baseline or an explicit overlay.
type DefinitionPolicySource string

const (
	// DefinitionPolicySourceDefault means the effective state is derived
	// from the definition's own baseline fields.
	DefinitionPolicySourceDefault DefinitionPolicySource = "default"
	// DefinitionPolicySourceOverlay means the effective state is driven
	// by a persisted runtime override.
	DefinitionPolicySourceOverlay DefinitionPolicySource = "overlay"
)

// DefinitionPolicy is the mutable runtime policy overlay for one loop
// definition.
type DefinitionPolicy struct {
	State     DefinitionPolicyState `yaml:"state,omitempty" json:"state,omitempty"`
	Reason    string                `yaml:"reason,omitempty" json:"reason,omitempty"`
	UpdatedAt time.Time             `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
}

// ParseDefinitionPolicyState validates a caller-provided definition
// policy state.
func ParseDefinitionPolicyState(raw string) (DefinitionPolicyState, error) {
	switch DefinitionPolicyState(strings.TrimSpace(raw)) {
	case DefinitionPolicyStateActive:
		return DefinitionPolicyStateActive, nil
	case DefinitionPolicyStatePaused:
		return DefinitionPolicyStatePaused, nil
	case DefinitionPolicyStateInactive:
		return DefinitionPolicyStateInactive, nil
	default:
		return "", fmt.Errorf("state must be one of [\"active\" \"paused\" \"inactive\"]")
	}
}

// InactiveDefinitionError reports that a loop definition exists but is
// currently disabled by effective runtime policy.
type InactiveDefinitionError struct {
	Name string
}

func (e *InactiveDefinitionError) Error() string {
	return fmt.Sprintf("loop: definition %q is inactive", e.Name)
}

// PausedDefinitionError reports that a loop definition exists but is
// currently paused by effective runtime policy.
type PausedDefinitionError struct {
	Name string
}

func (e *PausedDefinitionError) Error() string {
	return fmt.Sprintf("loop: definition %q is paused", e.Name)
}

// RunningDurableLoopOverridesError reports that the caller targeted a
// durable loop (service or container) that is already running and
// supplied either per-launch override fields or an inline spec that the
// runtime would silently drop. The remediation for scalar retunes is
// loop_definition_update (which applies live); structural changes go
// through loop_definition_set plus a stop+relaunch.
type RunningDurableLoopOverridesError struct {
	Name string
}

func (e *RunningDurableLoopOverridesError) Error() string {
	return fmt.Sprintf(
		"loop: durable definition %q is already running; "+
			"both per-launch overrides and inline launch.spec changes are dropped for active service and container loops. "+
			"For scalar retunes (task, model, instructions, sleep envelope, supervisor, max_iter), use loop_definition_update — it applies live to the running loop. "+
			"For structural changes, update the stored spec via loop_definition_set and restart with stop_loop + loop_definition_launch. "+
			"To just retrieve the loop ID, call loop_definition_launch with an empty launch ({}).",
		e.Name)
}

func defaultDefinitionPolicyState(spec Spec) DefinitionPolicyState {
	if spec.Enabled {
		return DefinitionPolicyStateActive
	}
	return DefinitionPolicyStateInactive
}

func effectiveDefinitionPolicy(spec Spec, policy DefinitionPolicy) (DefinitionPolicyState, DefinitionPolicySource) {
	if policy.State != "" {
		return policy.State, DefinitionPolicySourceOverlay
	}
	return defaultDefinitionPolicyState(spec), DefinitionPolicySourceDefault
}
