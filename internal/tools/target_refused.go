package tools

// ErrTargetRefused is returned when a write tool refuses the document a
// call named rather than what the call carried: the named record does
// not exist, or its write belongs in another document. Its text is the
// tool's own refusal, unchanged. The iterate engine tallies such a call
// under the empty target, the calls that wrote no document of their own,
// because no retry of the refused target is the write the model meant.
type ErrTargetRefused struct {
	Err error
}

// Error implements the error interface.
func (e *ErrTargetRefused) Error() string { return e.Err.Error() }

// Unwrap returns the tool's refusal.
func (e *ErrTargetRefused) Unwrap() error { return e.Err }
