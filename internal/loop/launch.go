package loop

import "fmt"

// Launch describes a single loops-ng launch request. It is separate
// from [Spec] so per-launch overrides and delivery hooks can grow here
// over time without turning [Spec] itself into an ephemeral run object.
type Launch struct {
	Spec Spec
}

// Validate checks that the launch is well-formed.
func (l *Launch) Validate() error {
	if l == nil {
		return fmt.Errorf("loop: launch is nil")
	}
	return l.Spec.Validate()
}

// LaunchResult is the outcome of starting a loop via [Registry.Launch].
// Request/reply launches wait for completion and return a final status;
// detached launches return immediately with the new loop ID.
type LaunchResult struct {
	LoopID      string    `json:"loop_id"`
	Operation   Operation `json:"operation"`
	Detached    bool      `json:"detached"`
	FinalStatus *Status   `json:"final_status,omitempty"`
}
