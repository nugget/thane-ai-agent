package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// TestHandleLoopOutputGetNegotiatesFidelity pins the #1250 HTTP read
// surface: text/plain is the status_line alone, application/json is
// the typed contract, anything else is the document body — and a
// plain-text request against a verdict-less document is 406 with the
// alternatives named, never an invented one-liner.
func TestHandleLoopOutputGetNegotiatesFidelity(t *testing.T) {
	t.Parallel()

	reg, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{{
		Name:      "metacognitive",
		Enabled:   true,
		Task:      "watch the watchers",
		Operation: looppkg.OperationService,
		Outputs: []looppkg.OutputSpec{{
			Name: "metacognitive_state",
			Type: looppkg.OutputTypeMaintainedDocument,
			Ref:  "self:metacognitive.md",
			Facets: []looppkg.FacetSpec{
				{Name: looppkg.OutputFacetStatusLine},
				{Name: looppkg.OutputFacetDigest},
			},
		}},
	}})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	body := "## Status Line\n\npanel clean, baselines steady\n\n## Digest\n\nNo open concerns.\n\n## Details\n\nworking memory\n"
	s := &Server{logger: slog.Default()}
	s.UseLoopDefinitionRegistry(reg)
	s.UseDocumentReader(func(_ context.Context, ref string) (*documents.DocumentRecord, error) {
		if ref != "self:metacognitive.md" {
			t.Fatalf("read unexpected ref %q", ref)
		}
		return &documents.DocumentRecord{Ref: ref, Body: body, ModifiedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
	})

	get := func(accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/loops/metacognitive/outputs/metacognitive_state", nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		req.SetPathValue("name", "metacognitive")
		req.SetPathValue("output", "metacognitive_state")
		rec := httptest.NewRecorder()
		s.handleLoopOutputGet(rec, req)
		return rec
	}

	plain := get("text/plain")
	if plain.Code != http.StatusOK || strings.TrimSpace(plain.Body.String()) != "panel clean, baselines steady" {
		t.Errorf("text/plain = %d %q, want the bare status_line", plain.Code, plain.Body.String())
	}

	typed := get("application/json")
	if typed.Code != http.StatusOK {
		t.Fatalf("application/json status = %d", typed.Code)
	}
	for _, want := range []string{`"status_line":"panel clean, baselines steady"`, `"No open concerns."`, `"full":"working memory`} {
		if !strings.Contains(typed.Body.String(), want) {
			t.Errorf("json body missing %s:\n%s", want, typed.Body.String())
		}
	}
	if strings.Contains(typed.Body.String(), "## Status Line") {
		t.Errorf("json body carries section markup:\n%s", typed.Body.String())
	}

	markdown := get("")
	if markdown.Code != http.StatusOK || !strings.Contains(markdown.Body.String(), "## Status Line") {
		t.Errorf("default representation should be the document body: %d %q", markdown.Code, markdown.Body.String())
	}

	// A verdict-less document refuses the plain representation.
	body = "# State\n\nno facets yet\n"
	refused := get("text/plain")
	if refused.Code != http.StatusNotAcceptable {
		t.Errorf("plain against verdict-less document = %d, want 406", refused.Code)
	}
	if !strings.Contains(refused.Body.String(), "application/json") {
		t.Errorf("406 should name the available representations: %q", refused.Body.String())
	}
}
