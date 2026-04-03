package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestOllamaError_EncodesJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	ollamaError(rec, http.StatusBadRequest, `model "gpt-oss:20b" is ambiguous; use one of ["mirror/gpt-oss:20b" "spark/gpt-oss:20b"]`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not valid JSON: %v; body=%q", err, rec.Body.String())
	}
	if payload["error"] == "" {
		t.Fatalf("error field missing from %q", rec.Body.String())
	}
}
