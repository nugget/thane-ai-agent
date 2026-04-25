package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
)

func TestAgentErrorDetails_UnknownModelIsBadRequest(t *testing.T) {
	code, message := agentErrorDetails(&fleet.UnknownModelError{Model: "missing/model"})

	if code != http.StatusBadRequest {
		t.Fatalf("code = %d, want %d", code, http.StatusBadRequest)
	}
	if message != `unknown model "missing/model"` {
		t.Fatalf("message = %q", message)
	}
}

func TestAgentErrorDetails_IncompatibleModelIsBadRequest(t *testing.T) {
	code, message := agentErrorDetails(&agent.IncompatibleModelError{
		Model:   "deepslate/google/gemma-3-4b",
		Reasons: []string{"this deployment is configured without tool support"},
	})

	if code != http.StatusBadRequest {
		t.Fatalf("code = %d, want %d", code, http.StatusBadRequest)
	}
	if !strings.Contains(message, "cannot satisfy this request") {
		t.Fatalf("message = %q, want incompatible-model detail", message)
	}
}

func TestAgentErrorDetails_NoEligibleModelIsBadRequest(t *testing.T) {
	code, message := agentErrorDetails(&agent.NoEligibleModelError{
		Requirement: "image inputs",
		Suggestions: []string{"deepslate/google/gemma-3-4b"},
	})

	if code != http.StatusBadRequest {
		t.Fatalf("code = %d, want %d", code, http.StatusBadRequest)
	}
	if !strings.Contains(message, "no eligible routed model supports image inputs") {
		t.Fatalf("message = %q", message)
	}
	if !strings.Contains(message, "deepslate/google/gemma-3-4b") {
		t.Fatalf("message = %q", message)
	}
}
