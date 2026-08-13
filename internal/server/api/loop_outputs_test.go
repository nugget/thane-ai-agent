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
	if plain.Header().Get("Vary") != "Accept" {
		t.Errorf("negotiated response missing Vary: Accept")
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

	// A multi-type header is negotiated, not substring-matched: axios's
	// default lists text/plain, but names application/json first, so the
	// typed contract wins.
	axios := get("application/json, text/plain, */*")
	if axios.Code != http.StatusOK || !strings.Contains(axios.Body.String(), `"status_line"`) {
		t.Errorf("axios default header should negotiate JSON: %d %q", axios.Code, axios.Body.String())
	}
	if ct := axios.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("axios default header Content-Type = %q, want application/json", ct)
	}

	// An undeclared contract refuses too: a body whose author typed a
	// reserved heading into a facet-less output is content, not
	// contract (#1372 review).
	regUndeclared, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{{
		Name:      "plainloop",
		Enabled:   true,
		Task:      "keep a document",
		Operation: looppkg.OperationService,
		Outputs: []looppkg.OutputSpec{{
			Name: "notes",
			Type: looppkg.OutputTypeMaintainedDocument,
			Ref:  "self:notes.md",
		}},
	}})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry (undeclared): %v", err)
	}
	s2 := &Server{logger: slog.Default()}
	s2.UseLoopDefinitionRegistry(regUndeclared)
	s2.UseDocumentReader(func(context.Context, string) (*documents.DocumentRecord, error) {
		return &documents.DocumentRecord{Ref: "self:notes.md", Body: "## Status Line\n\nan impostor verdict\n\n## Details\n\nbody\n"}, nil
	})
	reqU := httptest.NewRequest(http.MethodGet, "/v1/loops/plainloop/outputs/notes", nil)
	reqU.Header.Set("Accept", "text/plain")
	reqU.SetPathValue("name", "plainloop")
	reqU.SetPathValue("output", "notes")
	recU := httptest.NewRecorder()
	s2.handleLoopOutputGet(recU, reqU)
	if recU.Code != http.StatusNotAcceptable {
		t.Errorf("undeclared contract served text/plain: %d %q", recU.Code, recU.Body.String())
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

	// ...but a verdict-less document does NOT 406 a client that also
	// accepted JSON: falling through to a servable representation beats
	// refusing past one.
	fallthroughJSON := get("text/plain, application/json;q=0.5")
	if fallthroughJSON.Code != http.StatusOK || !strings.Contains(fallthroughJSON.Body.String(), `"full"`) {
		t.Errorf("plain-preferring client with JSON fallback = %d %q, want the typed contract", fallthroughJSON.Code, fallthroughJSON.Body.String())
	}
}

// TestNegotiateLoopOutputAccept pins the negotiation itself: q-values
// (including q=0 refusals), specificity, listed order, wildcards, and
// the servability fallthrough when no status_line is published.
func TestNegotiateLoopOutputAccept(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		header     string
		statusLine bool
		want       string
	}{
		// The absent/empty/unrecognized header serves the document.
		{"no header", "", true, loopOutputMarkdown},
		{"unrecognized type", "image/png", true, loopOutputMarkdown},
		{"malformed entry", "not-a-media-type", true, loopOutputMarkdown},

		// Exact single types are honored.
		{"plain only", "text/plain", true, loopOutputPlain},
		{"json only", "application/json", true, loopOutputJSONType},
		{"markdown only", "text/markdown", true, loopOutputMarkdown},
		{"case and spacing", "  Application/JSON ", true, loopOutputJSONType},

		// q-values order candidates; q=0 refuses a type outright.
		{"higher q wins", "text/plain;q=0.3, application/json;q=0.9", true, loopOutputJSONType},
		{"plain preferred by q", "application/json;q=0.1, text/plain;q=0.9", true, loopOutputPlain},
		{"q zero refuses plain", "text/plain;q=0, application/json", true, loopOutputJSONType},
		{"q zero refuses markdown", "text/markdown;q=0", true, ""},
		{"wildcard with plain refused", "*/*, text/plain;q=0", true, loopOutputMarkdown},
		{"malformed q ignored", "application/json;q=banana", true, loopOutputJSONType},

		// Specificity: an exact type outranks a wildcard at equal q.
		{"exact beats wildcard", "application/json, */*", true, loopOutputJSONType},
		{"subtype wildcard", "text/*", true, loopOutputMarkdown},
		{"full wildcard serves the default", "*/*", true, loopOutputMarkdown},

		// Listed order breaks exact-match ties: axios's default header
		// names application/json first and must get JSON.
		{"axios default", "application/json, text/plain, */*", true, loopOutputJSONType},
		{"plain listed first", "text/plain, application/json", true, loopOutputPlain},

		// No published status_line: plain falls through to the next
		// acceptable type rather than 406ing past a servable one, and
		// 406s only when nothing else was acceptable.
		{"axios default without status line", "application/json, text/plain, */*", false, loopOutputJSONType},
		{"plain first with json fallback", "text/plain, application/json", false, loopOutputJSONType},
		{"plain first with wildcard fallback", "text/plain, */*", false, loopOutputMarkdown},
		{"plain only without status line", "text/plain", false, ""},
		{"no header without status line", "", false, loopOutputMarkdown},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := negotiateLoopOutputAccept(tc.header, tc.statusLine); got != tc.want {
				t.Fatalf("negotiateLoopOutputAccept(%q, %v) = %q, want %q", tc.header, tc.statusLine, got, tc.want)
			}
		})
	}
}
