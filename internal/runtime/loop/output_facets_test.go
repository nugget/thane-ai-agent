package loop

import (
	"strings"
	"testing"
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

func TestOutputSpecTierFieldsFollowCanonicalOrder(t *testing.T) {
	tests := []struct {
		name   string
		output OutputSpec
		want   []string
	}{
		{
			name:   "full ladder",
			output: facetedOutput(OutputFacetStatusLine, OutputFacetTeaser, OutputFacetDigest),
			want:   []string{"status_line", "teaser", "digest", "full"},
		},
		{
			name:   "status line only still gets full",
			output: facetedOutput(OutputFacetStatusLine),
			want:   []string{"status_line", "full"},
		},
		{
			name:   "declaration order is ignored",
			output: facetedOutput(OutputFacetDigest, OutputFacetStatusLine),
			want:   []string{"status_line", "digest", "full"},
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

func TestValidateTierPayload(t *testing.T) {
	full := facetedOutput(OutputFacetStatusLine, OutputFacetTeaser, OutputFacetDigest)
	good := FacetPayload{
		StatusLine: "Sensors nominal; gate closed.",
		Teaser:     "Two troughs trending low since the heat spike.",
		Digest:     "Water levels are holding except troughs 2 and 5.",
		Full:       "# Ranch\n\n### Water\n\nDetail.",
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
			payload: FacetPayload{StatusLine: good.StatusLine, Digest: good.Digest, Full: good.Full},
			wantErr: "teaser is required",
		},
		{
			name:    "missing full",
			output:  full,
			payload: FacetPayload{StatusLine: good.StatusLine, Teaser: good.Teaser, Digest: good.Digest},
			wantErr: "full is required",
		},
		{
			name:    "undeclared facet is not required",
			output:  facetedOutput(OutputFacetStatusLine),
			payload: FacetPayload{StatusLine: good.StatusLine, Full: good.Full},
		},
		{
			name:    "status line rejects newline",
			output:  full,
			payload: FacetPayload{StatusLine: "One line\nTwo line", Teaser: good.Teaser, Digest: good.Digest, Full: good.Full},
			wantErr: "single line",
		},
		{
			name:    "over budget status line rejected",
			output:  full,
			payload: FacetPayload{StatusLine: strings.Repeat("a", statusLineMaxRunes+1), Teaser: good.Teaser, Digest: good.Digest, Full: good.Full},
			wantErr: "the limit is 120",
		},
		{
			name:    "budget counts runes not bytes",
			output:  full,
			payload: FacetPayload{StatusLine: strings.Repeat("é", statusLineMaxRunes), Teaser: good.Teaser, Digest: good.Digest, Full: good.Full},
		},
		{
			name:    "over budget teaser rejected",
			output:  full,
			payload: FacetPayload{StatusLine: good.StatusLine, Teaser: strings.Repeat("b", teaserMaxRunes+1), Digest: good.Digest, Full: good.Full},
			wantErr: "the limit is 500",
		},
		{
			name:    "full is unbounded",
			output:  full,
			payload: FacetPayload{StatusLine: good.StatusLine, Teaser: good.Teaser, Digest: good.Digest, Full: strings.Repeat("c", digestMaxRunes*4)},
		},
		{
			name:    "reserved heading in projection rejected",
			output:  full,
			payload: FacetPayload{StatusLine: good.StatusLine, Teaser: good.Teaser, Digest: "## Details\n\nsneaky", Full: good.Full},
			wantErr: "reserved section heading",
		},
		{
			name:    "deeper heading inside full is content",
			output:  full,
			payload: FacetPayload{StatusLine: good.StatusLine, Teaser: good.Teaser, Digest: good.Digest, Full: "### Teaser\n\nA subsection named like a facet is fine."},
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

func TestValidateTierPayloadRejectsUnfacetedOutput(t *testing.T) {
	out := OutputSpec{Name: "state", Type: OutputTypeMaintainedDocument, Ref: "core:state.md"}
	if err := out.ValidateFacetPayload(FacetPayload{Full: "body"}); err == nil {
		t.Fatal("ValidateFacetPayload() error = nil for an unfaceted output, want error")
	}
}

func TestRenderTierDocumentUsesCanonicalSections(t *testing.T) {
	out := facetedOutput(OutputFacetStatusLine, OutputFacetTeaser)
	body := out.RenderFacetDocument(FacetPayload{
		StatusLine: "All clear.",
		Teaser:     "Nothing needs attention today.",
		Digest:     "This facet is not declared and must not render.",
		Full:       "Everything is fine.",
	})

	want := "## Status Line\n\nAll clear.\n\n## Teaser\n\nNothing needs attention today.\n\n## Details\n\nEverything is fine."
	if body != want {
		t.Fatalf("RenderFacetDocument() =\n%q\nwant\n%q", body, want)
	}
}

// TestTierDocumentRoundTrip is the guarantee the whole storage decision
// rests on: the rendered document is the canonical store, so any derived
// binding can be re-seeded by parsing it back.
func TestTierDocumentRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		output  OutputSpec
		payload FacetPayload
	}{
		{
			name:   "full ladder",
			output: facetedOutput(OutputFacetStatusLine, OutputFacetTeaser, OutputFacetDigest),
			payload: FacetPayload{
				StatusLine: "Sensors nominal; 2 waters below 40%.",
				Teaser:     "Two troughs trending low since yesterday's heat spike.",
				Digest:     "Troughs 2 and 5 are below 40%.\n\nRefill likely needed by Friday.",
				Full:       "# Ranch Office\n\n### Water\n\nTrough detail.\n\n### Power\n\nStable.",
			},
		},
		{
			name:   "status line only",
			output: facetedOutput(OutputFacetStatusLine),
			payload: FacetPayload{
				StatusLine: "All clear.",
				Full:       "Nothing of note.",
			},
		},
		{
			name:   "multibyte content survives",
			output: facetedOutput(OutputFacetStatusLine, OutputFacetTeaser),
			payload: FacetPayload{
				StatusLine: "Café météo: stable — 21°C.",
				Teaser:     "Les capteurs sont à jour.",
				Full:       "Détails complets.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.output.ValidateFacetPayload(tt.payload); err != nil {
				t.Fatalf("payload should be valid: %v", err)
			}
			got := ParseTierDocument(tt.output.RenderFacetDocument(tt.payload))
			if got != tt.payload {
				t.Fatalf("round trip changed the payload:\ngot  %#v\nwant %#v", got, tt.payload)
			}
		})
	}
}

func TestParseTierDocumentAdoptsUnfacetedBody(t *testing.T) {
	// An existing maintained document being adopted into the faceted
	// contract has no recognized sections; its whole body is the full
	// projection rather than being lost.
	body := "# Ranch Office\n\nEverything written before facets existed."
	got := ParseTierDocument(body)
	if got.Full != body {
		t.Fatalf("Full = %q, want the whole legacy body", got.Full)
	}
	if got.StatusLine != "" || got.Teaser != "" || got.Digest != "" {
		t.Fatalf("legacy body should not populate projections: %#v", got)
	}
}

func TestParseTierDocumentFoldsPreambleIntoFull(t *testing.T) {
	body := "Stray lead paragraph.\n\n## Status Line\n\nAll clear.\n\n## Details\n\nBody proper."
	got := ParseTierDocument(body)
	if got.StatusLine != "All clear." {
		t.Fatalf("StatusLine = %q", got.StatusLine)
	}
	if got.Full != "Stray lead paragraph.\n\nBody proper." {
		t.Fatalf("Full = %q, want preamble folded ahead of the details body", got.Full)
	}
}

func TestParseTierDocumentAcceptsHeadingCaseDrift(t *testing.T) {
	// An operator hand-editing the document may not match our casing.
	got := ParseTierDocument("## status line\n\nAll clear.\n\n## DETAILS\n\nBody.")
	if got.StatusLine != "All clear." || got.Full != "Body." {
		t.Fatalf("case-insensitive heading match failed: %#v", got)
	}
}

func TestFacetedOutputToolNameUsesPublishVerb(t *testing.T) {
	faceted := facetedOutput(OutputFacetStatusLine)
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
