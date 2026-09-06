package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
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

// TestHandleLoopOutputGetExpandsTemporalTemplatesInEveryRepresentation
// pins the reader-side expansion at the HTTP boundary. Consumption
// reads render "{{delta:...}}" as a live delta; authoring reads keep it
// byte-exact, so the source record this handler was handed must come
// back out of the call unmodified.
//
// All three representations are asserted because they take different
// paths through the handler: text/plain serves a parsed facet, JSON
// serves parsed facets plus the parsed full, and text/markdown serves
// the whole body. Expansion moved back after ParseFacetSections would
// still satisfy the markdown case while shipping raw braces to the
// other two.
func TestHandleLoopOutputGetExpandsTemporalTemplatesInEveryRepresentation(t *testing.T) {
	t.Parallel()

	reg, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{{
		Name:      "whereabouts_nugget",
		Enabled:   true,
		Task:      "track one contact",
		Operation: looppkg.OperationService,
		Outputs: []looppkg.OutputSpec{{
			Name: "whereabouts",
			Type: looppkg.OutputTypeMaintainedDocument,
			Ref:  "whereabouts:nugget.md",
			Facets: []looppkg.FacetSpec{
				{Name: looppkg.OutputFacetStatusLine},
				{Name: looppkg.OutputFacetDigest},
			},
		}},
	}})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	// One instant for the whole test, so both template forms render a
	// fixed answer: the RFC3339 instant is 2d3h behind it, and the bare
	// date is two calendar days ahead of it.
	const (
		instantTemplate = "{{delta:2026-09-04T09:00:00Z}}"
		dateTemplate    = "{{delta:2026-09-08}}"
		wantInstant     = "-2d3h"
		wantDate        = "+2d"
	)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	stored := &documents.DocumentRecord{
		Ref: "whereabouts:nugget.md",
		Body: "## Status Line\n\naway since " + instantTemplate + ", back " + dateTemplate + "\n\n" +
			"## Digest\n\nFollow-up " + dateTemplate + ".\n\n" +
			"## Details\n\nDeparted " + instantTemplate + "; the follow-up lands " + dateTemplate + ".\n",
		ModifiedAt: now.Format(time.RFC3339Nano),
	}
	sourceBody := stored.Body

	s := &Server{logger: slog.Default(), nowFunc: func() time.Time { return now }}
	s.UseLoopDefinitionRegistry(reg)
	s.UseDocumentReader(func(_ context.Context, ref string) (*documents.DocumentRecord, error) {
		if ref != stored.Ref {
			t.Errorf("read unexpected ref %q", ref)
		}
		// Deliberately the same record every call: a handler that
		// expands in place would corrupt whatever the reader owns.
		return stored, nil
	})

	get := func(accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/loops/whereabouts_nugget/outputs/whereabouts", nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		req.SetPathValue("name", "whereabouts_nugget")
		req.SetPathValue("output", "whereabouts")
		rec := httptest.NewRecorder()
		s.handleLoopOutputGet(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("Accept %q = %d %q", accept, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "{{delta:") {
			t.Errorf("Accept %q served an unexpanded template:\n%s", accept, rec.Body.String())
		}
		return rec
	}

	wantStatusLine := "away since " + wantInstant + ", back " + wantDate

	if got := strings.TrimSpace(get("text/plain").Body.String()); got != wantStatusLine {
		t.Errorf("text/plain = %q, want %q", got, wantStatusLine)
	}

	var typed loopOutputJSON
	if err := json.Unmarshal(get("application/json").Body.Bytes(), &typed); err != nil {
		t.Fatalf("decode json representation: %v", err)
	}
	if got := typed.Facets["status_line"]; got != wantStatusLine {
		t.Errorf("json status_line = %q, want %q", got, wantStatusLine)
	}
	if got, want := typed.Facets["digest"], "Follow-up "+wantDate+"."; got != want {
		t.Errorf("json digest = %q, want %q", got, want)
	}
	if got, want := strings.TrimSpace(typed.Full), "Departed "+wantInstant+"; the follow-up lands "+wantDate+"."; got != want {
		t.Errorf("json full = %q, want %q", got, want)
	}

	wantMarkdown := strings.NewReplacer(instantTemplate, wantInstant, dateTemplate, wantDate).Replace(sourceBody)
	if got := get("text/markdown").Body.String(); got != wantMarkdown {
		t.Errorf("text/markdown = %q, want %q", got, wantMarkdown)
	}

	// The authoring round-trip depends on this: three consumption reads
	// must leave the document exactly as the store handed it over.
	if stored.Body != sourceBody {
		t.Errorf("handler rewrote the source document:\n got %q\nwant %q", stored.Body, sourceBody)
	}
}

// TestTemplateNowInZone pins which day "today" means on this surface.
// Day-word rendering is a calendar comparison, so expanding in the
// wrong zone renders tomorrow's date as "today" through the last hours
// of a local evening — the exact failure the household zone exists to
// prevent.
func TestTemplateNowInZone(t *testing.T) {
	t.Parallel()

	// 03:00 UTC on the 7th is still the evening of the 6th in Chicago.
	now := time.Date(2026, 9, 7, 3, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		zone    string
		wantDay int
	}{
		{"household zone decides the day", "America/Chicago", 6},
		{"unset zone leaves the instant alone", "", 7},
		{"whitespace is not a zone", "   ", 7},
		{"unloadable zone falls back", "Mars/Olympus_Mons", 7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := templateNowInZone(now, tc.zone)
			if !got.Equal(now) {
				t.Errorf("templateNowInZone moved the instant: %v, want %v", got, now)
			}
			if got.Day() != tc.wantDay {
				t.Errorf("templateNowInZone(%q).Day() = %d, want %d", tc.zone, got.Day(), tc.wantDay)
			}
		})
	}
}

// TestReaderTemplateNowWithoutALoop pins the degenerate wiring: no loop
// means no household zone to ask for, and the surface still has to
// answer with an instant rather than panic.
func TestReaderTemplateNowWithoutALoop(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 7, 3, 0, 0, 0, time.UTC)
	s := &Server{logger: slog.Default(), nowFunc: func() time.Time { return now }}
	if got := s.readerTemplateNow(); !got.Equal(now) {
		t.Errorf("readerTemplateNow without a loop = %v, want %v", got, now)
	}
}

// refusingLLM satisfies [llm.Client] so a test can stand up a real
// *agent.Loop. Every method refuses rather than answering: these tests
// never take a turn, so a call here means the test drifted into
// exercising the model instead of the handler.
type refusingLLM struct{}

func (refusingLLM) Chat(context.Context, string, []llm.Message, []map[string]any) (*llm.ChatResponse, error) {
	return nil, errors.New("refusingLLM: handler tests do not take turns")
}

func (refusingLLM) ChatStream(context.Context, string, []llm.Message, []map[string]any, llm.StreamCallback) (*llm.ChatResponse, error) {
	return nil, errors.New("refusingLLM: handler tests do not take turns")
}

func (refusingLLM) Ping(context.Context) error { return nil }

// TestHandleLoopOutputGetExpandsInTheHouseholdZone runs the expansion
// through the production wiring — a real *agent.Loop carrying a
// non-UTC household zone — rather than through templateNowInZone
// directly. The pure-function test pins what the zone math does; this
// pins that the handler actually asks the loop for a zone. Without
// that call the two tests would both stay green while every date
// regressed to UTC, which is wrong for the household for the last
// hours of every evening.
func TestHandleLoopOutputGetExpandsInTheHouseholdZone(t *testing.T) {
	t.Parallel()

	loop, err := agent.NewLoop(agent.LoopOptions{
		Logger:   slog.Default(),
		Memory:   memory.NewStore(8),
		LLM:      refusingLLM{},
		Model:    "test-model",
		Timezone: "America/Chicago",
	})
	if err != nil {
		t.Fatalf("agent.NewLoop: %v", err)
	}

	reg, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{{
		Name:      "whereabouts_nugget",
		Enabled:   true,
		Task:      "track one contact",
		Operation: looppkg.OperationService,
		Outputs: []looppkg.OutputSpec{{
			Name:   "whereabouts",
			Type:   looppkg.OutputTypeMaintainedDocument,
			Ref:    "whereabouts:nugget.md",
			Facets: []looppkg.FacetSpec{{Name: looppkg.OutputFacetStatusLine}},
		}},
	}})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	// 03:00 UTC on the 7th is 22:00 on the 6th in Chicago: the two
	// zones disagree about today's date, which is exactly the window
	// the household zone exists to get right. Expanding in UTC renders
	// this "yesterday" while the household is still living the day.
	now := time.Date(2026, 9, 7, 3, 0, 0, 0, time.UTC)

	s := &Server{logger: slog.Default(), loop: loop, nowFunc: func() time.Time { return now }}
	s.UseLoopDefinitionRegistry(reg)
	s.UseDocumentReader(func(_ context.Context, ref string) (*documents.DocumentRecord, error) {
		return &documents.DocumentRecord{
			Ref:  ref,
			Body: "## Status Line\n\nhome since {{delta:2026-09-06}}\n\n## Details\n\nbody\n",
		}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/loops/whereabouts_nugget/outputs/whereabouts", nil)
	req.Header.Set("Accept", "text/plain")
	req.SetPathValue("name", "whereabouts_nugget")
	req.SetPathValue("output", "whereabouts")
	rec := httptest.NewRecorder()
	s.handleLoopOutputGet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %q", rec.Code, rec.Body.String())
	}
	if got, want := strings.TrimSpace(rec.Body.String()), "home since today"; got != want {
		t.Errorf("text/plain = %q, want %q — expanded in %s, not the household zone",
			got, want, now.Location())
	}
}
