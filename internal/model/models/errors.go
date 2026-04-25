package models

import (
	"errors"
	"fmt"
)

// UnknownModelError reports that a caller referenced a model or
// deployment ID that is not present in the current catalog.
type UnknownModelError struct {
	Model string
}

func (e *UnknownModelError) Error() string {
	return fmt.Sprintf("unknown model %q", e.Model)
}

// IsUnknownModel reports whether err identifies a missing model
// reference or deployment ID.
func IsUnknownModel(err error) bool {
	var target *UnknownModelError
	return errors.As(err, &target)
}
