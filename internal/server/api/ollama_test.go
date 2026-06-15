package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
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

func TestOllamaAgentError_SurfacesProviderBadRequest(t *testing.T) {
	code, message := ollamaAgentError(fmt.Errorf(`API error 400: {"error":"context too long"}`))

	if code != http.StatusBadRequest {
		t.Fatalf("code = %d, want %d", code, http.StatusBadRequest)
	}
	if !strings.Contains(message, "context too long") {
		t.Fatalf("message = %q, want provider error details", message)
	}
}

func TestOllamaAgentError_EmptyCompletionIsBadGateway(t *testing.T) {
	code, message := ollamaAgentError(fmt.Errorf(`LM Studio returned an empty assistant completion for model "deepslate/google/gemma-3-4b"`))

	if code != http.StatusBadGateway {
		t.Fatalf("code = %d, want %d", code, http.StatusBadGateway)
	}
	if !strings.Contains(message, "empty assistant completion") {
		t.Fatalf("message = %q, want empty completion details", message)
	}
}

// TestHandleGenerate_NotImplemented verifies the deprecated /api/generate
// endpoint rejects prompt-based requests with 501 and a JSON error that points
// callers at /api/chat, rather than silently misdecoding the body as an empty
// chat turn. The handler must not touch the agent loop, so a nil-loop server is
// sufficient.
func TestHandleGenerate_NotImplemented(t *testing.T) {
	s := &OllamaServer{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	req := httptest.NewRequest(http.MethodPost, "/api/generate", strings.NewReader(`{"model":"thane","prompt":"hello"}`))
	rec := httptest.NewRecorder()

	s.handleGenerate(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}

	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not valid JSON: %v; body=%q", err, rec.Body.String())
	}
	if !strings.Contains(payload["error"], "/api/chat") {
		t.Fatalf("error = %q, want it to direct callers to /api/chat", payload["error"])
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
