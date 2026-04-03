package models

import (
	"errors"
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
	Routable  *bool
	Reason    string
	UpdatedAt time.Time
}

// UnknownDeploymentError reports that a requested deployment ID does not
// exist in the current effective registry snapshot.
type UnknownDeploymentError struct {
	Deployment string
}

func (e *UnknownDeploymentError) Error() string {
	return fmt.Sprintf("unknown deployment %q; query /v1/model-registry for valid deployment IDs", e.Deployment)
}

// IsUnknownDeployment reports whether err identifies a missing deployment ID.
func IsUnknownDeployment(err error) bool {
	var target *UnknownDeploymentError
	return errors.As(err, &target)
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
