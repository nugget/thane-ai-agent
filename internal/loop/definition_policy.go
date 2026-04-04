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
	case DefinitionPolicyStateInactive:
		return DefinitionPolicyStateInactive, nil
	default:
		return "", fmt.Errorf("state must be one of [\"active\" \"inactive\"]")
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

func cloneDefinitionPolicies(src map[string]DefinitionPolicy) map[string]DefinitionPolicy {
	dst := make(map[string]DefinitionPolicy, len(src))
	for key, policy := range src {
		dst[key] = policy
	}
	return dst
}
