package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRegisterOpenAIRoutes_ServesOpenAIShape verifies the OpenAI-compat server
// serves the OpenAI-shaped model list at /v1/models — distinct from the native
// fleet view served at /v1/models on the primary port. Registration must also
// not panic (no overlapping patterns in the OpenAI route set).
func TestRegisterOpenAIRoutes_ServesOpenAIShape(t *testing.T) {
	s := &Server{logger: testAPILogger()}
	mux := http.NewServeMux()
	s.RegisterOpenAIRoutes(mux)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["object"] != "list" {
		t.Errorf("object = %v, want \"list\" (OpenAI shape, not the native fleet array)", body["object"])
	}
}
