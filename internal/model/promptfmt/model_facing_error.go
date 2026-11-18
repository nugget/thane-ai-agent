package promptfmt

import "errors"

// ModelFacingError pairs an error with the text a model should read
// about it. Error returns the wrapped error's text unchanged, so logs
// and operators see exactly what they always did, while code that
// hands an error to a model (a tool result, for one) calls
// [ModelFacingText] and shows Text instead.
//
// Text is written for the model: what failed, in fixed words, and what
// it can do next. It must not carry file paths, hosts, model names or
// raw upstream responses, which belong to the operator's error.
type ModelFacingError struct {
	Text string
	Err  error
}

// Error returns the wrapped error's text.
func (e *ModelFacingError) Error() string { return e.Err.Error() }

// Unwrap exposes the wrapped error to errors.Is and errors.As.
func (e *ModelFacingError) Unwrap() error { return e.Err }

// ModelFacingText returns the Text of the first [ModelFacingError] in
// err's chain, and false when there is none.
func ModelFacingText(err error) (string, bool) {
	var mfe *ModelFacingError
	if errors.As(err, &mfe) && mfe.Text != "" {
		return mfe.Text, true
	}
	return "", false
}
