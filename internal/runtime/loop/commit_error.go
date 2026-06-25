package loop

import "fmt"

// CommitStage identifies which step of a durable loop-definition commit
// failed. Transport layers (the tool surface, the HTTP API) use it to map
// a commit failure to the right response without re-implementing the
// persist → register → reconcile sequence themselves.
type CommitStage string

const (
	// CommitStagePersist is the durable-store write.
	CommitStagePersist CommitStage = "persist"
	// CommitStageRegister is the in-memory overlay upsert. Its failures
	// are caller-facing (a bad spec, or an immutable-config conflict).
	CommitStageRegister CommitStage = "register"
	// CommitStageReconcile is the live-loop reconciliation.
	CommitStageReconcile CommitStage = "reconcile"
)

// CommitError tags a loop-definition commit failure with the stage that
// produced it. It unwraps to the underlying error, so typed checks such as
// errors.As for *ImmutableDefinitionError keep working through it.
type CommitError struct {
	Stage CommitStage
	Err   error
}

// Error reproduces the wording each stage used before the commit sequence
// was centralized: the register (overlay upsert) error is returned bare
// because its messages — ImmutableDefinitionError, spec validation — are
// already self-describing and callers historically saw them unprefixed,
// while persist and reconcile failures carry a stage prefix for context.
func (e *CommitError) Error() string {
	if e == nil {
		return "<nil>"
	}
	// Guard a nil Err: Error() must never panic, since it runs on every
	// logging/formatting path that touches a commit failure.
	if e.Err == nil {
		return fmt.Sprintf("%s loop definition: <nil>", e.Stage)
	}
	if e.Stage == CommitStageRegister {
		return e.Err.Error()
	}
	return fmt.Sprintf("%s loop definition: %s", e.Stage, e.Err)
}

// Unwrap exposes the underlying error for errors.Is / errors.As.
func (e *CommitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
