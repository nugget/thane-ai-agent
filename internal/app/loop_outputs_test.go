package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestHydrateLoopOutputsBuildsScopedToolsAndContext(t *testing.T) {
	t.Parallel()

	store, coreDir := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store}
	spec := looppkg.Spec{
		Name:       "metacognitive",
		Enabled:    true,
		Task:       "Maintain state.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
		Outputs: []looppkg.OutputSpec{
			{
				Name:    "metacognitive_state",
				Type:    looppkg.OutputTypeMaintainedDocument,
				Ref:     "core:metacognitive.md",
				Purpose: "Current metacognitive state.",
			},
			{
				Name: "metacognitive_notes",
				Type: looppkg.OutputTypeWorkingNotes,
				Ref:  "core:metacognitive-notes.md",
			},
		},
	}

	hydrated, err := app.hydrateLoopOutputs(spec)
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	if len(hydrated.RuntimeTools) != 2 {
		t.Fatalf("RuntimeTools len = %d, want 2", len(hydrated.RuntimeTools))
	}
	if hydrated.RuntimeTools[0].Name != "replace_output_metacognitive_state" {
		t.Fatalf("RuntimeTools[0].Name = %q", hydrated.RuntimeTools[0].Name)
	}
	if hydrated.RuntimeTools[1].Name != "replace_output_metacognitive_notes" {
		t.Fatalf("RuntimeTools[1].Name = %q", hydrated.RuntimeTools[1].Name)
	}
	for _, tool := range hydrated.RuntimeTools {
		if !tool.SkipContentResolve {
			t.Fatalf("%s SkipContentResolve = false, want true", tool.Name)
		}
	}

	_, err = hydrated.RuntimeTools[0].Handler(context.Background(), map[string]any{
		"body": "## Current Sense\n\nEverything is calm.",
	})
	if err != nil {
		t.Fatalf("replace output handler: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(coreDir, "metacognitive.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(raw), "Everything is calm.") {
		t.Fatalf("metacognitive.md = %s, want replacement content", string(raw))
	}

	_, err = hydrated.RuntimeTools[1].Handler(context.Background(), map[string]any{
		"body": "Current view: conditions are quiet and the trend holds.",
	})
	if err != nil {
		t.Fatalf("notes handler: %v", err)
	}
	notes, err := os.ReadFile(filepath.Join(coreDir, "metacognitive-notes.md"))
	if err != nil {
		t.Fatalf("ReadFile notes: %v", err)
	}
	if !strings.Contains(string(notes), "conditions are quiet") {
		t.Fatalf("notes = %s, want the rewritten body", string(notes))
	}

	ctx, err := hydrated.OutputContextBuilder(context.Background(), hydrated.Outputs)
	if err != nil {
		t.Fatalf("OutputContextBuilder: %v", err)
	}
	for _, want := range []string{
		"Declared Durable Outputs",
		"replace_output_metacognitive_state",
		"replace_output_metacognitive_notes",
		"Everything is calm.",
		"conditions are quiet",
	} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("output context missing %q:\n%s", want, ctx)
		}
	}
}

func TestCloneLoopOutputsDeepCopiesFacets(t *testing.T) {
	src := []looppkg.OutputSpec{{
		Name:   "ranch status",
		Type:   looppkg.OutputTypeMaintainedDocument,
		Ref:    "kb:ranch.md",
		Facets: []looppkg.FacetSpec{{Name: looppkg.OutputFacetStatusLine}, {Name: looppkg.OutputFacetTeaser}},
	}}
	dst := cloneLoopOutputs(src)
	dst[0].Facets[1] = looppkg.FacetSpec{Name: looppkg.OutputFacetDigest}
	if src[0].Facets[1].Name != looppkg.OutputFacetTeaser {
		t.Fatalf("cloneLoopOutputs shares Facets backing array: src mutated to %q", src[0].Facets[1])
	}
}

func TestRenderLoopOutputContextUsesDeltaFreshness(t *testing.T) {
	t.Parallel()

	store, coreDir := newLoopOutputDocumentStore(t)
	now := time.Date(2026, 5, 21, 20, 0, 0, 0, time.UTC)
	raw := `---
created: 2026-05-21T17:00:00Z
updated: 2026-05-21T18:00:00Z
---

## Current Sense

Everything is calm.
`
	if err := os.WriteFile(filepath.Join(coreDir, "metacognitive.md"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write metacognitive.md: %v", err)
	}

	ctx, err := renderLoopOutputContextWithNow(context.Background(), store, []looppkg.OutputSpec{
		{
			Name:    "metacognitive_state",
			Type:    looppkg.OutputTypeMaintainedDocument,
			Ref:     "core:metacognitive.md",
			Purpose: "Current metacognitive state.",
		},
	}, now)
	if err != nil {
		t.Fatalf("renderLoopOutputContextWithNow: %v", err)
	}

	if !strings.Contains(ctx, `"updated_delta": "-2h"`) {
		t.Fatalf("output context missing delta freshness:\n%s", ctx)
	}
	for _, unwanted := range []string{
		`"modified_at"`,
		"created:",
		"updated:",
		"2026-05-21T18:00:00Z",
	} {
		if strings.Contains(ctx, unwanted) {
			t.Fatalf("output context contains raw timestamp metadata %q:\n%s", unwanted, ctx)
		}
	}
	if !strings.Contains(ctx, "Everything is calm.") {
		t.Fatalf("output context should include stripped document body:\n%s", ctx)
	}
}

func TestRenderLoopOutputContextUsesAuthoredProjections(t *testing.T) {
	t.Parallel()

	store, coreDir := newLoopOutputDocumentStore(t)
	now := time.Date(2026, 8, 12, 20, 0, 0, 0, time.UTC)
	facetedSpec := looppkg.OutputSpec{
		Name:    "metacognitive_state",
		Type:    looppkg.OutputTypeMaintainedDocument,
		Ref:     "core:metacognitive.md",
		Purpose: "Current metacognitive state.",
		Facets: []looppkg.FacetSpec{
			{Name: looppkg.OutputFacetStatusLine},
			{Name: looppkg.OutputFacetDigest},
		},
	}

	raw := "## Status Line\n\npanel clean, baselines steady\n\n## Digest\n\nNo open concerns.\n\n## Details\n\nthe working memory body\n"
	if err := os.WriteFile(filepath.Join(coreDir, "metacognitive.md"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write metacognitive.md: %v", err)
	}

	ctx, err := renderLoopOutputContextWithNow(context.Background(), store, []looppkg.OutputSpec{facetedSpec}, now)
	if err != nil {
		t.Fatalf("renderLoopOutputContextWithNow: %v", err)
	}
	// The authored projections ride whole under their own key, and the
	// content field carries only the Details body — never a blind byte
	// slice of the rendered document (#1250).
	if !strings.Contains(ctx, `"projections"`) {
		t.Fatalf("faceted output context missing projections:\n%s", ctx)
	}
	if !strings.Contains(ctx, "panel clean, baselines steady") || !strings.Contains(ctx, "No open concerns.") {
		t.Fatalf("projections missing authored values:\n%s", ctx)
	}
	if !strings.Contains(ctx, "the working memory body") {
		t.Fatalf("content missing the Details body:\n%s", ctx)
	}
	if strings.Contains(ctx, `## Status Line`) {
		t.Fatalf("content should not carry the rendered section structure:\n%s", ctx)
	}
	// full is unbudgeted and lives only in content; a "full" key under
	// projections would smuggle the whole Details body past the byte
	// budget (the regression review caught in the first draft).
	if strings.Contains(ctx, `"full":`) {
		t.Fatalf("projections must not carry the full body:\n%s", ctx)
	}

	// A pre-facet body under a faceted spec — declared facets, first
	// publish pending — keeps the legacy whole-body path so the loop
	// still sees the document it must carry into that first publish.
	preFacet := "# State\n\nbaselines and concerns, pre-facet shape\n"
	if err := os.WriteFile(filepath.Join(coreDir, "metacognitive.md"), []byte(preFacet), 0o644); err != nil {
		t.Fatalf("rewrite metacognitive.md: %v", err)
	}
	ctx, err = renderLoopOutputContextWithNow(context.Background(), store, []looppkg.OutputSpec{facetedSpec}, now)
	if err != nil {
		t.Fatalf("renderLoopOutputContextWithNow (pre-facet): %v", err)
	}
	if strings.Contains(ctx, `"projections"`) {
		t.Fatalf("pre-facet document must not invent projections:\n%s", ctx)
	}
	if !strings.Contains(ctx, "baselines and concerns, pre-facet shape") {
		t.Fatalf("pre-facet content missing body:\n%s", ctx)
	}
}

func TestHydrateLoopOutputsRequiresDocumentStore(t *testing.T) {
	t.Parallel()

	_, err := (&App{}).hydrateLoopOutputs(looppkg.Spec{
		Name: "writer",
		Task: "Maintain output.",
		Outputs: []looppkg.OutputSpec{
			{Name: "status", Type: looppkg.OutputTypeMaintainedDocument, Ref: "core:status.md"},
		},
	})
	if err == nil {
		t.Fatal("hydrateLoopOutputs error = nil, want missing document roots error")
	}
	if !strings.Contains(err.Error(), "managed document roots are not configured") {
		t.Fatalf("error = %v", err)
	}
}

func TestTruncateLoopOutputTextPreservesUTF8(t *testing.T) {
	t.Parallel()

	const text = "abcédef"
	head, truncated, shown, total := truncateLoopOutputText(text, 4, false)
	if !truncated {
		t.Fatal("head truncated = false, want true")
	}
	if !utf8.ValidString(head) {
		t.Fatalf("head is invalid UTF-8: %q", head)
	}
	if !strings.HasPrefix(head, "abc\n") {
		t.Fatalf("head = %q, want ASCII prefix before split rune", head)
	}
	if shown != len(head) || total != len(text) {
		t.Fatalf("head counts shown=%d total=%d, want %d/%d", shown, total, len(head), len(text))
	}

	tail, truncated, shown, total := truncateLoopOutputText(text, 4, true)
	if !truncated {
		t.Fatal("tail truncated = false, want true")
	}
	if !utf8.ValidString(tail) {
		t.Fatalf("tail is invalid UTF-8: %q", tail)
	}
	if !strings.HasSuffix(tail, "def") {
		t.Fatalf("tail = %q, want suffix after split rune", tail)
	}
	if shown != len(tail) || total != len(text) {
		t.Fatalf("tail counts shown=%d total=%d, want %d/%d", shown, total, len(tail), len(text))
	}
}

func newLoopOutputDocumentStore(t *testing.T) (*documents.Store, string) {
	t.Helper()

	rootDir := t.TempDir()
	coreDir := filepath.Join(rootDir, "core")
	if err := os.MkdirAll(coreDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := documents.NewStore(db, map[string]string{"core": coreDir}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store, coreDir
}

// TestReExposeNativeTools pins the read-side re-exposure contract: an
// output-declaring loop gets the native read tools verbatim, a name the
// registry doesn't carry degrades to absence rather than failing the
// launch, and a tool without a handler is never re-exposed (the
// compiled runtime layer would drop it silently later — skipping it
// here keeps the surface honest at the seam that decided it).
func TestReExposeNativeTools(t *testing.T) {
	registry := tools.NewEmptyRegistry()
	registry.Register(&tools.Tool{
		Name:        "doc_read",
		Description: "Read a managed document.",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(context.Context, map[string]any) (string, error) {
			return "ok", nil
		},
	})
	registry.Register(&tools.Tool{
		Name:        "doc_history",
		Description: "Revision history.",
		Handler:     nil, // never re-exposed
	})

	got := reExposeNativeTools(registry, ownOutputReadToolNames)
	if len(got) != 1 {
		t.Fatalf("re-exposed %d tools, want exactly the one with a handler: %+v", len(got), got)
	}
	if got[0].Name != "doc_read" || got[0].Handler == nil {
		t.Errorf("re-exposed tool = %+v, want doc_read with its handler", got[0])
	}
	if got[0].Description != "Read a managed document." {
		t.Errorf("description = %q, want the native's verbatim", got[0].Description)
	}

	if extra := reExposeNativeTools(nil, ownOutputReadToolNames); extra != nil {
		t.Errorf("nil registry should re-expose nothing, got %+v", extra)
	}
}

// TestHydrateLoopOutputsWithoutAgentLoop pins the degradation path: an
// App whose agent loop isn't wired (early boot, minimal tests) still
// hydrates output tools — the read-side re-exposure is additive, never
// a launch dependency.
func TestHydrateLoopOutputsWithoutAgentLoop(t *testing.T) {
	store, _ := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store}
	spec := looppkg.Spec{
		Name:      "reader",
		Enabled:   true,
		Task:      "curate",
		Operation: looppkg.OperationService,
		SleepMin:  time.Minute,
		SleepMax:  time.Hour,
		Outputs: []looppkg.OutputSpec{{
			Name: "state",
			Type: looppkg.OutputTypeMaintainedDocument,
			Ref:  "core:state.md",
		}},
	}
	hydrated, err := app.hydrateLoopOutputs(spec)
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	names := make([]string, 0, len(hydrated.RuntimeTools))
	for _, rt := range hydrated.RuntimeTools {
		names = append(names, rt.Name)
	}
	if !slices.Contains(names, "replace_output_state") {
		t.Errorf("output tool missing: %v", names)
	}
	if slices.Contains(names, "doc_read") {
		t.Errorf("no agent loop wired, so no native to re-expose; got %v", names)
	}
}

// TestWrapOwnOutputDocRead pins the owner privilege and its exact
// boundary: a whole-document read of the loop's own output returns in
// full past the general 16 KiB cap, while a foreign ref and a
// facet-level read fall through to the native handler untouched. The
// cap the privilege replaces is a real one — the same read through the
// standard path truncates — so this test builds a document that
// actually exceeds it.
func TestWrapOwnOutputDocRead(t *testing.T) {
	store, _ := newLoopOutputDocumentStore(t)
	docTools := documents.NewTools(store)

	big := strings.Repeat("The office stays warm and the belief accumulates. ", 500) // ~25 KiB
	if _, err := store.Write(context.Background(), documents.WriteArgs{
		Ref:  "core:office.md",
		Body: &big,
	}); err != nil {
		t.Fatalf("seed large document: %v", err)
	}

	nativeCalls := 0
	runtimeTools := []looppkg.RuntimeTool{{
		Name:        "doc_read",
		Description: "native description.",
		Handler: func(context.Context, map[string]any) (string, error) {
			nativeCalls++
			return `{"native": true}`, nil
		},
	}}
	outputs := []looppkg.OutputSpec{{
		Name: "office",
		Type: looppkg.OutputTypeMaintainedDocument,
		Ref:  "core:office.md",
	}}
	wrapped := wrapOwnOutputDocRead(runtimeTools, docTools, outputs)

	// Own ref, no level: full body under the raised budget.
	got, err := wrapped[0].Handler(context.Background(), map[string]any{"ref": "core:office.md"})
	if err != nil {
		t.Fatalf("own-output read: %v", err)
	}
	if strings.Contains(got, `"truncated": true`) {
		t.Fatalf("own-output read truncated under the privileged budget:\n%.300s", got)
	}
	if !strings.Contains(got, "the belief accumulates") {
		t.Errorf("own-output read missing body content:\n%.300s", got)
	}
	if nativeCalls != 0 {
		t.Errorf("own-output read went through the native capped path")
	}

	// The privilege replaces a real cap: the standard path truncates
	// this same document.
	capped, err := docTools.Read(context.Background(), documents.RefArgs{Ref: "core:office.md"})
	if err != nil {
		t.Fatalf("standard read: %v", err)
	}
	if !strings.Contains(capped, `"truncated": true`) {
		t.Errorf("standard read did not truncate a >16KiB document; the privilege tests nothing:\n%.200s", capped)
	}

	// Foreign ref: native path.
	if _, err := wrapped[0].Handler(context.Background(), map[string]any{"ref": "core:other.md"}); err != nil {
		t.Fatalf("foreign read: %v", err)
	}
	if nativeCalls != 1 {
		t.Errorf("foreign ref should use the native handler, calls = %d", nativeCalls)
	}

	// Own ref with level: native path (a projection never nears the cap).
	if _, err := wrapped[0].Handler(context.Background(), map[string]any{"ref": "core:office.md", "level": "status_line"}); err != nil {
		t.Fatalf("level read: %v", err)
	}
	if nativeCalls != 2 {
		t.Errorf("level read should use the native handler, calls = %d", nativeCalls)
	}

	if !strings.Contains(wrapped[0].Description, "raised result budget") {
		t.Errorf("wrapped description should teach the privilege: %q", wrapped[0].Description)
	}
}

// TestReplaceOutputRejectsOversizedBody pins the write-side ceiling at
// the replace tool: what the loop cannot read back whole, it must not
// be able to write in the first place.
func TestReplaceOutputRejectsOversizedBody(t *testing.T) {
	store, _ := newLoopOutputDocumentStore(t)
	runtimeTools := buildLoopOutputTools(store, []looppkg.OutputSpec{{
		Name: "state",
		Type: looppkg.OutputTypeMaintainedDocument,
		Ref:  "core:state.md",
	}})
	if len(runtimeTools) != 1 {
		t.Fatalf("tools = %d, want 1", len(runtimeTools))
	}
	_, err := runtimeTools[0].Handler(context.Background(), map[string]any{
		"body": strings.Repeat("x", looppkg.MaxOutputDocumentBytes+1),
	})
	if err == nil {
		t.Fatal("oversized body should refuse")
	}
	if !strings.Contains(err.Error(), "outgrown single-document maintenance") {
		t.Errorf("error should teach the restructure: %v", err)
	}
	// Nothing was written: the refusal is the whole outcome.
	if _, readErr := store.Read(context.Background(), "core:state.md"); readErr == nil {
		t.Error("refused write still created the document")
	}
}
