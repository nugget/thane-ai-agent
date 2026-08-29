package loop

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOutputSpecValidateAndToolName(t *testing.T) {
	tests := []struct {
		name     string
		output   OutputSpec
		wantTool string
		wantErr  bool
	}{
		{
			name: "maintained document defaults replace",
			output: OutputSpec{
				Name: "Metacognitive State",
				Type: OutputTypeMaintainedDocument,
				Ref:  "core:metacognitive.md",
			},
			wantTool: "replace_output_metacognitive_state",
		},
		{
			name: "missing ref",
			output: OutputSpec{
				Name: "state",
				Type: OutputTypeMaintainedDocument,
			},
			wantErr: true,
		},
		{
			name: "non ascii name rejected when it cannot produce a suffix",
			output: OutputSpec{
				Name: "état",
				Type: OutputTypeMaintainedDocument,
				Ref:  "core:state.md",
			},
			wantErr: true,
		},
		{
			name: "tab rejected",
			output: OutputSpec{
				Name: "bad\tname",
				Type: OutputTypeMaintainedDocument,
				Ref:  "core:state.md",
			},
			wantErr: true,
		},
		{
			name: "newline rejected",
			output: OutputSpec{
				Name: "bad\nname",
				Type: OutputTypeMaintainedDocument,
				Ref:  "core:state.md",
			},
			wantErr: true,
		},
		{
			name: "overlong tool name rejected",
			output: OutputSpec{
				Name: strings.Repeat("a", maxOutputToolNameLength),
				Type: OutputTypeMaintainedDocument,
				Ref:  "core:state.md",
			},
			wantErr: true,
		},
		{
			// Notes hold what the loop currently believes, so it rewrites
			// them rather than appending to a history it would have to
			// re-read to find its own present view.
			name: "working notes default to replace",
			output: OutputSpec{
				Name: "ranch notes",
				Type: OutputTypeWorkingNotes,
				Ref:  "core:ranch-notes.md",
			},
			wantTool: "replace_output_ranch_notes",
		},
		{
			name: "working notes rejects published audience",
			output: OutputSpec{
				Name:     "ranch notes",
				Type:     OutputTypeWorkingNotes,
				Ref:      "core:ranch-notes.md",
				Audience: OutputAudiencePublished,
			},
			wantErr: true,
		},
		{
			name: "unknown audience rejected",
			output: OutputSpec{
				Name:     "state",
				Type:     OutputTypeMaintainedDocument,
				Ref:      "core:state.md",
				Audience: OutputAudience("secret"),
			},
			wantErr: true,
		},
		{
			name: "faceted maintained document publishes projections",
			output: OutputSpec{
				Name:   "ranch status",
				Type:   OutputTypeMaintainedDocument,
				Ref:    "core:ranch.md",
				Facets: []FacetSpec{{Name: OutputFacetStatusLine}, {Name: OutputFacetTeaser}, {Name: OutputFacetDigest}},
			},
			wantTool: "publish_output_ranch_status",
		},
		{
			name: "status line alone anchors the ladder",
			output: OutputSpec{
				Name:   "ranch status",
				Type:   OutputTypeMaintainedDocument,
				Ref:    "core:ranch.md",
				Facets: []FacetSpec{{Name: OutputFacetStatusLine}},
			},
			wantTool: "publish_output_ranch_status",
		},
		{
			name: "facets without status line rejected",
			output: OutputSpec{
				Name:   "ranch status",
				Type:   OutputTypeMaintainedDocument,
				Ref:    "core:ranch.md",
				Facets: []FacetSpec{{Name: OutputFacetTeaser}, {Name: OutputFacetDigest}},
			},
			wantErr: true,
		},
		{
			name: "unknown facet rejected",
			output: OutputSpec{
				Name:   "ranch status",
				Type:   OutputTypeMaintainedDocument,
				Ref:    "core:ranch.md",
				Facets: []FacetSpec{{Name: OutputFacetStatusLine}, {Name: OutputFacet("hud")}},
			},
			wantErr: true,
		},
		{
			name: "duplicate facet rejected",
			output: OutputSpec{
				Name:   "ranch status",
				Type:   OutputTypeMaintainedDocument,
				Ref:    "core:ranch.md",
				Facets: []FacetSpec{{Name: OutputFacetStatusLine}, {Name: OutputFacetStatusLine}},
			},
			wantErr: true,
		},
		{
			name: "facets on working notes rejected",
			output: OutputSpec{
				Name:   "ranch notes",
				Type:   OutputTypeWorkingNotes,
				Ref:    "core:ranch-notes.md",
				Facets: []FacetSpec{{Name: OutputFacetStatusLine}},
			},
			wantErr: true,
		},
		{
			name: "facets on internal maintained document rejected",
			output: OutputSpec{
				Name:     "hypotheses",
				Type:     OutputTypeMaintainedDocument,
				Ref:      "core:hypotheses.md",
				Audience: OutputAudienceInternal,
				Facets:   []FacetSpec{{Name: OutputFacetStatusLine}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.output.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("Validate() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if got := tt.output.ToolName(); got != tt.wantTool {
				t.Fatalf("ToolName() = %q, want %q", got, tt.wantTool)
			}
		})
	}
}

func TestOutputSpecEffectiveAudience(t *testing.T) {
	tests := []struct {
		name   string
		output OutputSpec
		want   OutputAudience
	}{
		{
			name:   "maintained document defaults published",
			output: OutputSpec{Type: OutputTypeMaintainedDocument},
			want:   OutputAudiencePublished,
		},
		{
			name:   "working notes default internal",
			output: OutputSpec{Type: OutputTypeWorkingNotes},
			want:   OutputAudienceInternal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.output.EffectiveAudience(); got != tt.want {
				t.Fatalf("EffectiveAudience() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCloneOutputsDeepCopiesFacets(t *testing.T) {
	src := []OutputSpec{{
		Name:   "ranch status",
		Type:   OutputTypeMaintainedDocument,
		Ref:    "core:ranch.md",
		Facets: []FacetSpec{{Name: OutputFacetStatusLine}, {Name: OutputFacetTeaser}},
	}}
	dst := cloneOutputs(src)
	dst[0].Facets[1] = FacetSpec{Name: OutputFacetDigest}
	if src[0].Facets[1].Name != OutputFacetTeaser {
		t.Fatalf("cloneOutputs shares Facets backing array: src mutated to %q", src[0].Facets[1])
	}
}

func TestOutputSpecValidateRefGrammar(t *testing.T) {
	// Guards #1068: content resolution could replace a real ref with the
	// referenced document's body, leaving a multi-line markdown blob in
	// Ref. Validate must reject that while accepting every well-formed ref.
	tests := []struct {
		name    string
		ref     string
		wantErr bool
	}{
		{name: "simple core ref", ref: "core:metacognitive.md"},
		{name: "nested path ref", ref: "projects:ranch-operations/ranch-climate-watch.md"},
		{name: "kb ref", ref: "kb:dashboards/pr-watchlist.md"},
		{name: "generated ref", ref: "generated:daily/digest.md"},
		// The production corruption signature: a whole document, frontmatter
		// and all, sitting where the ref should be.
		{name: "frontmatter blob rejected", ref: "---\ntitle: \"Ranch Climate Watch\"\ncreated: \"2026-06-25T03:45:49Z\"\n---\n\n# body", wantErr: true},
		{name: "embedded newline rejected", ref: "core:state.md\nextra", wantErr: true},
		{name: "carriage return rejected", ref: "core:state.md\r\nmore", wantErr: true},
		{name: "nul byte rejected", ref: "core:sta\x00te.md", wantErr: true},
		{name: "other control char rejected", ref: "core:sta\x07te.md", wantErr: true},
		{name: "no root separator rejected", ref: "metacognitive.md", wantErr: true},
		{name: "empty root rejected", ref: ":state.md", wantErr: true},
		{name: "empty path rejected", ref: "core:", wantErr: true},
		{name: "root with whitespace rejected", ref: "--- title:state.md", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := OutputSpec{Name: "doc", Type: OutputTypeMaintainedDocument, Ref: tt.ref}
			err := out.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Validate() error = nil for ref %q, want error", tt.ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() error = %v for ref %q, want nil", err, tt.ref)
			}
		})
	}
}

func TestSpecJSONRoundTripIncludesOutputs(t *testing.T) {
	spec := Spec{
		Name:       "writer",
		Enabled:    true,
		Task:       "Maintain output.",
		Operation:  OperationService,
		Completion: CompletionNone,
		Outputs: []OutputSpec{
			{
				Name:    "status",
				Type:    OutputTypeMaintainedDocument,
				Ref:     "generated:status.md",
				Purpose: "Current status.",
				Facets:  []FacetSpec{{Name: OutputFacetStatusLine}, {Name: OutputFacetTeaser}},
			},
			{
				Name: "notes",
				Type: OutputTypeWorkingNotes,
				Ref:  "generated:notes.md",
			},
		},
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"outputs"`) {
		t.Fatalf("marshaled spec missing outputs: %s", string(data))
	}
	if !strings.Contains(string(data), `"facets"`) {
		t.Fatalf("marshaled spec missing facets: %s", string(data))
	}

	var got Spec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Outputs) != 2 {
		t.Fatalf("Outputs len = %d, want 2", len(got.Outputs))
	}
	if got.Outputs[0].ToolName() != "publish_output_status" {
		t.Fatalf("output tool = %q, want publish_output_status for a faceted output", got.Outputs[0].ToolName())
	}
	if len(got.Outputs[0].Facets) != 2 || got.Outputs[0].Facets[0].Name != OutputFacetStatusLine {
		t.Fatalf("round-tripped facets = %v, want [status_line teaser]", got.Outputs[0].Facets)
	}
	if got.Outputs[1].EffectiveAudience() != OutputAudienceInternal {
		t.Fatalf("working notes audience = %q, want internal", got.Outputs[1].EffectiveAudience())
	}
	if err := got.ValidatePersistable(); err != nil {
		t.Fatalf("ValidatePersistable: %v", err)
	}
}

func TestSpecValidateRejectsDuplicateOutputToolNames(t *testing.T) {
	spec := Spec{
		Name:       "writer",
		Enabled:    true,
		Task:       "Maintain output.",
		Operation:  OperationService,
		Completion: CompletionNone,
		Outputs: []OutputSpec{
			{Name: "status-report", Type: OutputTypeMaintainedDocument, Ref: "generated:a.md"},
			{Name: "status report", Type: OutputTypeMaintainedDocument, Ref: "generated:b.md"},
		},
	}

	err := spec.ValidatePersistable()
	if err == nil {
		t.Fatal("ValidatePersistable() error = nil, want duplicate tool error")
	}
	if !strings.Contains(err.Error(), "duplicate generated tool") {
		t.Fatalf("error = %v, want duplicate generated tool", err)
	}
}
