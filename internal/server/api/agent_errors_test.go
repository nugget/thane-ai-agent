package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/models"
)

func TestAgentErrorDetails_UnknownModelIsBadRequest(t *testing.T) {
	code, message := agentErrorDetails(&models.UnknownModelError{Model: "missing/model"})

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
