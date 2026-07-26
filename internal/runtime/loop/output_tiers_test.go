package loop

import (
	"strings"
	"testing"
)

func tieredOutput(tiers ...OutputTier) OutputSpec {
	return OutputSpec{
		Name:  "ranch status",
		Type:  OutputTypeMaintainedDocument,
		Ref:   "core:ranch.md",
		Tiers: tiers,
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
			output: tieredOutput(OutputTierStatusLine, OutputTierTeaser, OutputTierDigest),
			want:   []string{"status_line", "teaser", "digest", "full"},
		},
		{
			name:   "status line only still gets full",
			output: tieredOutput(OutputTierStatusLine),
			want:   []string{"status_line", "full"},
		},
		{
			name:   "declaration order is ignored",
			output: tieredOutput(OutputTierDigest, OutputTierStatusLine),
			want:   []string{"status_line", "digest", "full"},
		},
		{
			name:   "untiered output has no fields",
			output: OutputSpec{Name: "state", Type: OutputTypeMaintainedDocument, Ref: "core:state.md"},
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := tt.output.TierFields()
			got := make([]string, 0, len(fields))
			for _, field := range fields {
				got = append(got, field.Key)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("TierFields() keys = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateTierPayload(t *testing.T) {
	full := tieredOutput(OutputTierStatusLine, OutputTierTeaser, OutputTierDigest)
	good := TierPayload{
		StatusLine: "Sensors nominal; gate closed.",
		Teaser:     "Two troughs trending low since the heat spike.",
		Digest:     "Water levels are holding except troughs 2 and 5.",
		Full:       "# Ranch\n\n### Water\n\nDetail.",
	}

	tests := []struct {
		name    string
		output  OutputSpec
		payload TierPayload
		wantErr string
	}{
		{name: "complete payload", output: full, payload: good},
		{
			name:    "missing declared tier",
			output:  full,
			payload: TierPayload{StatusLine: good.StatusLine, Digest: good.Digest, Full: good.Full},
			wantErr: "teaser is required",
		},
		{
			name:    "missing full",
			output:  full,
			payload: TierPayload{StatusLine: good.StatusLine, Teaser: good.Teaser, Digest: good.Digest},
			wantErr: "full is required",
		},
		{
			name:    "undeclared tier is not required",
			output:  tieredOutput(OutputTierStatusLine),
			payload: TierPayload{StatusLine: good.StatusLine, Full: good.Full},
		},
		{
			name:    "status line rejects newline",
			output:  full,
			payload: TierPayload{StatusLine: "One line\nTwo line", Teaser: good.Teaser, Digest: good.Digest, Full: good.Full},
			wantErr: "single line",
		},
		{
			name:    "over budget status line rejected",
			output:  full,
			payload: TierPayload{StatusLine: strings.Repeat("a", statusLineMaxRunes+1), Teaser: good.Teaser, Digest: good.Digest, Full: good.Full},
			wantErr: "the limit is 120",
		},
		{
			name:    "budget counts runes not bytes",
			output:  full,
			payload: TierPayload{StatusLine: strings.Repeat("é", statusLineMaxRunes), Teaser: good.Teaser, Digest: good.Digest, Full: good.Full},
		},
		{
			name:    "over budget teaser rejected",
			output:  full,
			payload: TierPayload{StatusLine: good.StatusLine, Teaser: strings.Repeat("b", teaserMaxRunes+1), Digest: good.Digest, Full: good.Full},
			wantErr: "the limit is 500",
		},
		{
			name:    "full is unbounded",
			output:  full,
			payload: TierPayload{StatusLine: good.StatusLine, Teaser: good.Teaser, Digest: good.Digest, Full: strings.Repeat("c", digestMaxRunes*4)},
		},
		{
			name:    "reserved heading in projection rejected",
			output:  full,
			payload: TierPayload{StatusLine: good.StatusLine, Teaser: good.Teaser, Digest: "## Details\n\nsneaky", Full: good.Full},
			wantErr: "reserved section heading",
		},
		{
			name:    "deeper heading inside full is content",
			output:  full,
			payload: TierPayload{StatusLine: good.StatusLine, Teaser: good.Teaser, Digest: good.Digest, Full: "### Teaser\n\nA subsection named like a tier is fine."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.output.ValidateTierPayload(tt.payload)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateTierPayload() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateTierPayload() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateTierPayload() error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateTierPayloadRejectsUntieredOutput(t *testing.T) {
	out := OutputSpec{Name: "state", Type: OutputTypeMaintainedDocument, Ref: "core:state.md"}
	if err := out.ValidateTierPayload(TierPayload{Full: "body"}); err == nil {
		t.Fatal("ValidateTierPayload() error = nil for an untiered output, want error")
	}
}

func TestRenderTierDocumentUsesCanonicalSections(t *testing.T) {
	out := tieredOutput(OutputTierStatusLine, OutputTierTeaser)
	body := out.RenderTierDocument(TierPayload{
		StatusLine: "All clear.",
		Teaser:     "Nothing needs attention today.",
		Digest:     "This tier is not declared and must not render.",
		Full:       "Everything is fine.",
	})

	want := "## Status Line\n\nAll clear.\n\n## Teaser\n\nNothing needs attention today.\n\n## Details\n\nEverything is fine."
	if body != want {
		t.Fatalf("RenderTierDocument() =\n%q\nwant\n%q", body, want)
	}
}

// TestTierDocumentRoundTrip is the guarantee the whole storage decision
// rests on: the rendered document is the canonical store, so any derived
// binding can be re-seeded by parsing it back.
func TestTierDocumentRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		output  OutputSpec
		payload TierPayload
	}{
		{
			name:   "full ladder",
			output: tieredOutput(OutputTierStatusLine, OutputTierTeaser, OutputTierDigest),
			payload: TierPayload{
				StatusLine: "Sensors nominal; 2 waters below 40%.",
				Teaser:     "Two troughs trending low since yesterday's heat spike.",
				Digest:     "Troughs 2 and 5 are below 40%.\n\nRefill likely needed by Friday.",
				Full:       "# Ranch Office\n\n### Water\n\nTrough detail.\n\n### Power\n\nStable.",
			},
		},
		{
			name:   "status line only",
			output: tieredOutput(OutputTierStatusLine),
			payload: TierPayload{
				StatusLine: "All clear.",
				Full:       "Nothing of note.",
			},
		},
		{
			name:   "multibyte content survives",
			output: tieredOutput(OutputTierStatusLine, OutputTierTeaser),
			payload: TierPayload{
				StatusLine: "Café météo: stable — 21°C.",
				Teaser:     "Les capteurs sont à jour.",
				Full:       "Détails complets.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.output.ValidateTierPayload(tt.payload); err != nil {
				t.Fatalf("payload should be valid: %v", err)
			}
			got := ParseTierDocument(tt.output.RenderTierDocument(tt.payload))
			if got != tt.payload {
				t.Fatalf("round trip changed the payload:\ngot  %#v\nwant %#v", got, tt.payload)
			}
		})
	}
}

func TestParseTierDocumentAdoptsUntieredBody(t *testing.T) {
	// An existing maintained document being adopted into the tiered
	// contract has no recognized sections; its whole body is the full
	// projection rather than being lost.
	body := "# Ranch Office\n\nEverything written before tiers existed."
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

func TestTieredOutputToolNameUsesPublishVerb(t *testing.T) {
	tiered := tieredOutput(OutputTierStatusLine)
	if got := tiered.ToolName(); got != "publish_output_ranch_status" {
		t.Fatalf("tiered ToolName() = %q, want publish_output_ranch_status", got)
	}
	untiered := OutputSpec{Name: "ranch status", Type: OutputTypeMaintainedDocument, Ref: "core:ranch.md"}
	if got := untiered.ToolName(); got != "replace_output_ranch_status" {
		t.Fatalf("untiered ToolName() = %q, want replace_output_ranch_status", got)
	}
	if len(tiered.ToolName()) > maxOutputToolNameLength {
		t.Fatalf("publish tool name exceeds the tool-name budget: %q", tiered.ToolName())
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
	if !strings.Contains(err.Error(), "one private log") {
		t.Fatalf("error = %v, want it to explain the single-log rule", err)
	}

	// The escape hatch the error names must actually validate.
	spec.Outputs[1] = OutputSpec{
		Name:     "shop journal",
		Type:     OutputTypeJournalDocument,
		Ref:      "core:shop-journal.md",
		Audience: OutputAudienceInternal,
	}
	if err := spec.ValidatePersistable(); err != nil {
		t.Fatalf("ValidatePersistable() with an internal journal alongside working notes: %v", err)
	}
}
