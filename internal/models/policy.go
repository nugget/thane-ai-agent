package models

import (
	"fmt"
	"strings"
	"time"
)

// DeploymentPolicyState describes the runtime policy state of a
// deployment in the mutable overlay.
type DeploymentPolicyState string

const (
	DeploymentPolicyStateActive   DeploymentPolicyState = "active"
	DeploymentPolicyStateInactive DeploymentPolicyState = "inactive"
	DeploymentPolicyStateFlagged  DeploymentPolicyState = "flagged"
)

// DeploymentPolicySource describes whether a deployment policy comes
// from the default baseline or from an explicit runtime overlay.
type DeploymentPolicySource string

const (
	DeploymentPolicySourceDefault DeploymentPolicySource = "default"
	DeploymentPolicySourceOverlay DeploymentPolicySource = "overlay"
)

// DeploymentPolicy is the mutable runtime policy overlay for one
// deployment.
type DeploymentPolicy struct {
	State     DeploymentPolicyState
	Reason    string
	UpdatedAt time.Time
}

// ParseDeploymentPolicyState validates a caller-provided policy state.
func ParseDeploymentPolicyState(raw string) (DeploymentPolicyState, error) {
	switch DeploymentPolicyState(strings.TrimSpace(raw)) {
	case DeploymentPolicyStateActive:
		return DeploymentPolicyStateActive, nil
	case DeploymentPolicyStateInactive:
		return DeploymentPolicyStateInactive, nil
	case DeploymentPolicyStateFlagged:
		return DeploymentPolicyStateFlagged, nil
	default:
		return "", fmt.Errorf("state must be one of [\"active\" \"inactive\" \"flagged\"]")
	}
}
