package loop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

func facetedOutput(names ...OutputFacet) OutputSpec {
	facets := make([]FacetSpec, 0, len(names))
	for _, name := range names {
		facets = append(facets, FacetSpec{Name: name})
	}
	return OutputSpec{
		Name:   "ranch status",
		Type:   OutputTypeMaintainedDocument,
		Ref:    "core:ranch.md",
		Facets: facets,
	}
}

func TestOutputSpecFacetFieldsFollowCanonicalOrder(t *testing.T) {
	tests := []struct {
		name   string
		output OutputSpec
		want   []string
	}{
		{
			name:   "every facet",
			output: facetedOutput(OutputFacetSignal, OutputFacetTeaser, OutputFacetDigest),
			want:   []string{"signal", "teaser", "digest", "full"},
		},
		{
			name:   "signal only still gets full",
			output: facetedOutput(OutputFacetSignal),
			want:   []string{"signal", "full"},
		},
		{
			name:   "declaration order is ignored",
			output: facetedOutput(OutputFacetDigest, OutputFacetSignal),
			want:   []string{"signal", "digest", "full"},
		},
		{
			name:   "unfaceted output has no fields",
			output: OutputSpec{Name: "state", Type: OutputTypeMaintainedDocument, Ref: "core:state.md"},
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := tt.output.FacetFields()
			got := make([]string, 0, len(fields))
			for _, field := range fields {
				got = append(got, field.Key)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("FacetFields() keys = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFacetContextRolesUnifyOutwardSignals(t *testing.T) {
	t.Parallel()

	want := map[string]agentctx.ContextProjectionRole{
		"signal": agentctx.ContextRoleSignal,
		"teaser": agentctx.ContextRoleSignal,
		"digest": agentctx.ContextRoleContext,
		"full":   agentctx.ContextRoleDetail,
	}
	for key, role := range want {
		field, ok := FacetFieldByKey(key)
		if !ok {
			t.Fatalf("FacetFieldByKey(%q) missing", key)
		}
		if field.ContextRole != role {
			t.Errorf("FacetFieldByKey(%q).ContextRole = %q, want %q", key, field.ContextRole, role)
		}
	}
}

func TestValidateFacetPayload(t *testing.T) {
	full := facetedOutput(OutputFacetSignal, OutputFacetTeaser, OutputFacetDigest)
	good := FacetPayload{
		Signal: "Sensors nominal; gate closed.",
		Teaser: "Two troughs trending low since the heat spike.",
		Digest: "Water levels are holding except troughs 2 and 5.",
		Full:   "# Ranch\n\n### Water\n\nDetail.",
	}

	tests := []struct {
		name    string
		output  OutputSpec
		payload FacetPayload
		wantErr string
	}{
		{name: "complete payload", output: full, payload: good},
		{
			name:    "missing declared facet",
			output:  full,
			payload: FacetPayload{Signal: good.Signal, Digest: good.Digest, Full: good.Full},
			wantErr: "teaser is required",
		},
		{
			name:    "missing full",
			output:  full,
			payload: FacetPayload{Signal: good.Signal, Teaser: good.Teaser, Digest: good.Digest},
			wantErr: "full is required",
		},
		{
			name:    "undeclared facet is not required",
			output:  facetedOutput(OutputFacetSignal),
			payload: FacetPayload{Signal: good.Signal, Full: good.Full},
		},
		{
			name:    "signal rejects newline",
			output:  full,
			payload: FacetPayload{Signal: "One line\nTwo line", Teaser: good.Teaser, Digest: good.Digest, Full: good.Full},
			wantErr: "single line",
		},
		{
			name:    "over budget signal rejected",
			output:  full,
			payload: FacetPayload{Signal: strings.Repeat("a", signalMaxRunes+1), Teaser: good.Teaser, Digest: good.Digest, Full: good.Full},
			wantErr: "the limit is 120",
		},
		{
			name:    "budget counts runes not bytes",
			output:  full,
			payload: FacetPayload{Signal: strings.Repeat("é", signalMaxRunes), Teaser: good.Teaser, Digest: good.Digest, Full: good.Full},
		},
		{
			name:    "over budget teaser rejected",
			output:  full,
			payload: FacetPayload{Signal: good.Signal, Teaser: strings.Repeat("b", teaserMaxRunes+1), Digest: good.Digest, Full: good.Full},
			wantErr: "the limit is 500",
		},
		{
			name:    "full is unbounded",
			output:  full,
			payload: FacetPayload{Signal: good.Signal, Teaser: good.Teaser, Digest: good.Digest, Full: strings.Repeat("c", digestMaxRunes*4)},
		},
		{
			name:    "reserved heading in projection rejected",
			output:  full,
			payload: FacetPayload{Signal: good.Signal, Teaser: good.Teaser, Digest: "## Details\n\nsneaky", Full: good.Full},
			wantErr: "reserved section heading",
		},
		{
			name:    "deeper heading inside full is content",
			output:  full,
			payload: FacetPayload{Signal: good.Signal, Teaser: good.Teaser, Digest: good.Digest, Full: "### Teaser\n\nA subsection named like a facet is fine."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.output.ValidateFacetPayload(tt.payload)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateFacetPayload() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateFacetPayload() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateFacetPayload() error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateFacetPayloadRejectsUnfacetedOutput(t *testing.T) {
	out := OutputSpec{Name: "state", Type: OutputTypeMaintainedDocument, Ref: "core:state.md"}
	if err := out.ValidateFacetPayload(FacetPayload{Full: "body"}); err == nil {
		t.Fatal("ValidateFacetPayload() error = nil for an unfaceted output, want error")
	}
}

func TestRenderFacetDocumentUsesCanonicalSections(t *testing.T) {
	out := facetedOutput(OutputFacetSignal, OutputFacetTeaser)
	body := out.RenderFacetDocument(FacetPayload{
		Signal: "All clear.",
		Teaser: "Nothing needs attention today.",
		Digest: "This facet is not declared and must not render.",
		Full:   "Everything is fine.",
	})

	want := "## Signal\n\nAll clear.\n\n## Teaser\n\nNothing needs attention today.\n\n## Details\n\nEverything is fine."
	if body != want {
		t.Fatalf("RenderFacetDocument() =\n%q\nwant\n%q", body, want)
	}
}

// TestFacetDocumentRoundTrip is the guarantee the whole storage decision
// rests on: the rendered document is the canonical store, so any derived
// binding can be re-seeded by parsing it back.
func TestFacetDocumentRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		output  OutputSpec
		payload FacetPayload
	}{
		{
			name:   "every facet",
			output: facetedOutput(OutputFacetSignal, OutputFacetTeaser, OutputFacetDigest),
			payload: FacetPayload{
				Signal: "Sensors nominal; 2 waters below 40%.",
				Teaser: "Two troughs trending low since yesterday's heat spike.",
				Digest: "Troughs 2 and 5 are below 40%.\n\nRefill likely needed by Friday.",
				Full:   "# Ranch Office\n\n### Water\n\nTrough detail.\n\n### Power\n\nStable.",
			},
		},
		{
			name:   "signal only",
			output: facetedOutput(OutputFacetSignal),
			payload: FacetPayload{
				Signal: "All clear.",
				Full:   "Nothing of note.",
			},
		},
		{
			name:   "multibyte content survives",
			output: facetedOutput(OutputFacetSignal, OutputFacetTeaser),
			payload: FacetPayload{
				Signal: "Café météo: stable — 21°C.",
				Teaser: "Les capteurs sont à jour.",
				Full:   "Détails complets.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.output.ValidateFacetPayload(tt.payload); err != nil {
				t.Fatalf("payload should be valid: %v", err)
			}
			got := tt.output.ParseFacetDocument(tt.output.RenderFacetDocument(tt.payload))
			if got != tt.payload {
				t.Fatalf("round trip changed the payload:\ngot  %#v\nwant %#v", got, tt.payload)
			}
		})
	}
}

func TestParseFacetDocumentAdoptsUnfacetedBody(t *testing.T) {
	// An existing maintained document being adopted into the faceted
	// contract has no recognized sections; its whole body is the full
	// projection rather than being lost.
	body := "# Ranch Office\n\nEverything written before facets existed."
	got := facetedOutput(OutputFacetSignal).ParseFacetDocument(body)
	if got.Full != body {
		t.Fatalf("Full = %q, want the whole legacy body", got.Full)
	}
	if got.Signal != "" || got.Teaser != "" || got.Digest != "" {
		t.Fatalf("legacy body should not populate projections: %#v", got)
	}
}

func TestParseFacetDocumentFoldsPreambleIntoFull(t *testing.T) {
	body := "Stray lead paragraph.\n\n## Signal\n\nAll clear.\n\n## Details\n\nBody proper."
	got := facetedOutput(OutputFacetSignal).ParseFacetDocument(body)
	if got.Signal != "All clear." {
		t.Fatalf("Signal = %q", got.Signal)
	}
	if got.Full != "Stray lead paragraph.\n\nBody proper." {
		t.Fatalf("Full = %q, want preamble folded ahead of the details body", got.Full)
	}
}

func TestParseFacetDocumentAcceptsHeadingCaseDrift(t *testing.T) {
	// An operator hand-editing the document may not match our casing.
	got := facetedOutput(OutputFacetSignal).ParseFacetDocument("## signal\n\nAll clear.\n\n## DETAILS\n\nBody.")
	if got.Signal != "All clear." || got.Full != "Body." {
		t.Fatalf("case-insensitive heading match failed: %#v", got)
	}
}

func TestFacetedOutputToolNameUsesPublishVerb(t *testing.T) {
	faceted := facetedOutput(OutputFacetSignal)
	if got := faceted.ToolName(); got != "publish_output_ranch_status" {
		t.Fatalf("faceted ToolName() = %q, want publish_output_ranch_status", got)
	}
	unfaceted := OutputSpec{Name: "ranch status", Type: OutputTypeMaintainedDocument, Ref: "core:ranch.md"}
	if got := unfaceted.ToolName(); got != "replace_output_ranch_status" {
		t.Fatalf("unfaceted ToolName() = %q, want replace_output_ranch_status", got)
	}
	if len(faceted.ToolName()) > maxOutputToolNameLength {
		t.Fatalf("publish tool name exceeds the tool-name budget: %q", faceted.ToolName())
	}
}

func TestValidateOutputsRejectsSecondWorkingNotes(t *testing.T) {
	spec := Spec{
		Name:       "curator",
		Enabled:    true,
		Task:       "Curate.",
		Operation:  OperationService,
		Completion: CompletionNone,
		Outputs: []OutputSpec{
			{Name: "office notes", Type: OutputTypeWorkingNotes, Ref: "core:office-notes.md"},
			{Name: "shop notes", Type: OutputTypeWorkingNotes, Ref: "core:shop-notes.md"},
		},
	}
	err := spec.ValidatePersistable()
	if err == nil {
		t.Fatal("ValidatePersistable() error = nil for two working_notes outputs, want error")
	}
	if !strings.Contains(err.Error(), "one place for its current thinking") {
		t.Fatalf("error = %v, want it to explain the single-notes rule", err)
	}

	// The escape hatch the error names must actually validate.
	spec.Outputs[1] = OutputSpec{
		Name:     "shop log",
		Type:     OutputTypeMaintainedDocument,
		Ref:      "core:shop-log.md",
		Audience: OutputAudienceInternal,
	}
	if err := spec.ValidatePersistable(); err != nil {
		t.Fatalf("ValidatePersistable() with an internal journal alongside working notes: %v", err)
	}
}

// TestRenderFacetScaffold pins the pre-first-publish body to the exact
// shape a correct publish produces: the declared section skeleton in
// ladder order, one placeholder per section. The first iteration reads
// this body from its output context, so a scaffold shaped differently
// from a published document would teach that turn the wrong form.
func TestRenderFacetScaffold(t *testing.T) {
	tests := []struct {
		name         string
		output       OutputSpec
		wantHeadings []string
		wantAbsent   []string
	}{
		{
			name:         "every facet renders the whole ladder",
			output:       facetedOutput(OutputFacetSignal, OutputFacetTeaser, OutputFacetDigest),
			wantHeadings: []string{"## Signal", "## Teaser", "## Digest", "## Details"},
		},
		{
			name:         "signal only still scaffolds full",
			output:       facetedOutput(OutputFacetSignal),
			wantHeadings: []string{"## Signal", "## Details"},
			wantAbsent:   []string{"## Teaser", "## Digest"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.output.RenderFacetScaffold()
			last := -1
			for _, heading := range tt.wantHeadings {
				idx := strings.Index(body, heading)
				if idx < 0 {
					t.Fatalf("scaffold missing %q:\n%s", heading, body)
				}
				if idx < last {
					t.Fatalf("scaffold headings out of ladder order:\n%s", body)
				}
				last = idx
			}
			for _, heading := range tt.wantAbsent {
				if strings.Contains(body, heading) {
					t.Errorf("scaffold has undeclared section %q:\n%s", heading, body)
				}
			}
			if !strings.Contains(body, "awaiting first cycle") {
				t.Errorf("scaffold placeholders missing awaiting-first-cycle marker:\n%s", body)
			}
		})
	}
}

// TestRenderFacetScaffoldRoundTrips confirms the scaffold parses back
// through the same reader a published document does, with a placeholder
// in every declared projection — a consumer asking for signal
// before the first cycle gets an honest placeholder, not an empty
// string or a parse failure.
func TestRenderFacetScaffoldRoundTrips(t *testing.T) {
	output := facetedOutput(OutputFacetSignal, OutputFacetTeaser, OutputFacetDigest)
	payload, found := ParseFacetSections(output.RenderFacetScaffold())
	if !found {
		t.Fatal("scaffold did not parse as a faceted document")
	}
	for key, value := range map[string]string{
		"signal": payload.Signal,
		"teaser": payload.Teaser,
		"digest": payload.Digest,
		"full":   payload.Full,
	} {
		if !strings.Contains(value, "awaiting first cycle") {
			t.Errorf("%s placeholder = %q, want awaiting-first-cycle text", key, value)
		}
	}
	// Budgeted placeholders must respect their own budgets: a scaffold
	// that overflows its facet would be rejected if republished.
	if err := output.ValidateFacetPayload(payload); err != nil {
		t.Errorf("scaffold payload fails its own contract: %v", err)
	}
}

// TestRenderFacetScaffoldJSONFacet pins the json-format placeholder:
// the section renders inside a json fence, so the placeholder must be
// valid JSON rather than prose.
func TestRenderFacetScaffoldJSONFacet(t *testing.T) {
	output := OutputSpec{
		Name:   "feed state",
		Type:   OutputTypeMaintainedDocument,
		Ref:    "core:feed.md",
		Facets: []FacetSpec{{Name: OutputFacetSignal, Format: FacetFormatJSON}},
	}
	body := output.RenderFacetScaffold()
	if !strings.Contains(body, "```json") {
		t.Fatalf("json facet scaffold missing fence:\n%s", body)
	}
	payload := output.ParseFacetDocument(body)
	if !json.Valid([]byte(payload.Signal)) {
		t.Errorf("json facet placeholder is not valid JSON: %q", payload.Signal)
	}
}

// TestValidateOutputBodySize pins the write-side half of the
// read-back invariant: a body past the ceiling is rejected with the
// restructure teaching, and the ceiling sits below the owner's
// privileged read budget so anything accepted here always reads back
// whole.
func TestValidateOutputBodySize(t *testing.T) {
	if err := ValidateOutputBodySize(strings.Repeat("x", MaxOutputDocumentBytes)); err != nil {
		t.Fatalf("body at the ceiling should pass: %v", err)
	}
	err := ValidateOutputBodySize(strings.Repeat("x", MaxOutputDocumentBytes+1))
	if err == nil {
		t.Fatal("body past the ceiling should refuse")
	}
	for _, want := range []string{"outgrown single-document maintenance", "read back what you write"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should teach the restructure, missing %q: %v", want, err)
		}
	}
}

// TestValidateFacetPayloadRejectsOversizedFull pins that the ceiling
// reaches the faceted contract: full is the only unbudgeted field, so
// it alone can push the document past what the owner reads back whole.
func TestValidateFacetPayloadRejectsOversizedFull(t *testing.T) {
	output := facetedOutput(OutputFacetSignal)
	payload := FacetPayload{
		Signal: "One line.",
		Full:   strings.Repeat("x", MaxOutputDocumentBytes+1),
	}
	err := output.ValidateFacetPayload(payload)
	if err == nil {
		t.Fatal("oversized full should refuse")
	}
	if !strings.Contains(err.Error(), "full:") || !strings.Contains(err.Error(), "outgrown") {
		t.Errorf("error should name full and teach the restructure: %v", err)
	}
}

// TestValidateFacetPayloadRejectsOversizedComposite pins the boundary
// the per-field check alone would miss: full inside the ceiling, but
// the rendered document — projections plus headings — past it.
func TestValidateFacetPayloadRejectsOversizedComposite(t *testing.T) {
	output := facetedOutput(OutputFacetSignal, OutputFacetDigest)
	payload := FacetPayload{
		Signal: "One standalone line of current state.",
		Digest: strings.Repeat("d", 2000),
		Full:   strings.Repeat("x", MaxOutputDocumentBytes-100),
	}
	err := output.ValidateFacetPayload(payload)
	if err == nil {
		t.Fatal("composite past the ceiling should refuse")
	}
	if !strings.Contains(err.Error(), "rendered document") || !strings.Contains(err.Error(), "full is the lever") {
		t.Errorf("error should attribute the composite and name the lever: %v", err)
	}
}
