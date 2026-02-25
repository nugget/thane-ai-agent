package tools

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrToolUnavailable_Error(t *testing.T) {
	err := &ErrToolUnavailable{ToolName: "web_search"}
	want := `tool "web_search" is not available in this context`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestErrToolUnavailable_ErrorsAs(t *testing.T) {
	orig := &ErrToolUnavailable{ToolName: "exec"}

	// errors.As should match the concrete type.
	var target *ErrToolUnavailable
	if !errors.As(orig, &target) {
		t.Fatal("errors.As failed to match *ErrToolUnavailable")
	}
	if target.ToolName != "exec" {
		t.Errorf("ToolName = %q, want %q", target.ToolName, "exec")
	}
}

func TestErrToolUnavailable_WrappedErrorsAs(t *testing.T) {
	orig := &ErrToolUnavailable{ToolName: "github_issue"}
	wrapped := fmt.Errorf("tool execution: %w", orig)

	var target *ErrToolUnavailable
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As failed to match wrapped *ErrToolUnavailable")
	}
	if target.ToolName != "github_issue" {
		t.Errorf("ToolName = %q, want %q", target.ToolName, "github_issue")
	}
}

func TestErrToolUnavailable_NotMatchOtherErrors(t *testing.T) {
	other := fmt.Errorf("some other error")
	var target *ErrToolUnavailable
	if errors.As(other, &target) {
		t.Error("errors.As should not match non-ErrToolUnavailable error")
	}
}
