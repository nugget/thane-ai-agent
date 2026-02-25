// Package tools provides the tool registry and execution framework.
//
// This file defines sentinel error types for tool execution.
package tools

import "fmt"

// ErrToolUnavailable is returned when a tool call targets a tool that
// is not present in the effective registry. This indicates a capability
// mismatch (filtered by tags, excluded by request, or nonexistent),
// not a transient execution failure. Callers should break the iteration
// loop rather than retrying.
type ErrToolUnavailable struct {
	ToolName string
}

// Error implements the error interface.
func (e *ErrToolUnavailable) Error() string {
	return fmt.Sprintf("tool %q is not available in this context", e.ToolName)
}
