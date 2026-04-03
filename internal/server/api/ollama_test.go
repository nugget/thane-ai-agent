package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/llm"
)

func TestOllamaAgentError_AmbiguousModel(t *testing.T) {
	code, message := ollamaAgentError(&llm.AmbiguousModelError{
		Model:   "gpt-oss:20b",
		Targets: []string{"mirror/gpt-oss:20b", "spark/gpt-oss:20b"},
	})

	if code != http.StatusBadRequest {
		t.Fatalf("code = %d, want %d", code, http.StatusBadRequest)
	}
	if message != `model "gpt-oss:20b" is ambiguous; use one of ["mirror/gpt-oss:20b" "spark/gpt-oss:20b"]` {
		t.Fatalf("message = %q", message)
	}
}

func TestOllamaAgentError_DefaultsToGeneric500(t *testing.T) {
	code, message := ollamaAgentError(fmt.Errorf("something broke"))

	if code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want %d", code, http.StatusInternalServerError)
	}
	if message != "agent error" {
		t.Fatalf("message = %q, want %q", message, "agent error")
	}
}
