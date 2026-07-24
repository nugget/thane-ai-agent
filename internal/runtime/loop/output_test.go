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
			name: "journal document defaults append",
			output: OutputSpec{
				Name: "service-journal",
				Type: OutputTypeJournalDocument,
				Ref:  "core:service-journal.md",
			},
			wantTool: "append_output_service_journal",
		},
		{
			name: "invalid mode for type",
			output: OutputSpec{
				Name: "state",
				Type: OutputTypeMaintainedDocument,
				Ref:  "core:state.md",
				Mode: OutputModeAppend,
			},
			wantErr: true,
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
			name: "working notes defaults append",
			output: OutputSpec{
				Name: "ranch notes",
				Type: OutputTypeWorkingNotes,
				Ref:  "core:ranch-notes.md",
			},
			wantTool: "append_output_ranch_notes",
		},
		{
			name: "working notes rejects replace mode",
			output: OutputSpec{
				Name: "ranch notes",
				Type: OutputTypeWorkingNotes,
				Ref:  "core:ranch-notes.md",
				Mode: OutputModeReplace,
			},
			wantErr: true,
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
			name: "journal accepts explicit internal audience",
			output: OutputSpec{
				Name:     "private journal",
				Type:     OutputTypeJournalDocument,
				Ref:      "core:private-journal.md",
				Audience: OutputAudienceInternal,
			},
			wantTool: "append_output_private_journal",
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
			name: "tiered maintained document valid",
			output: OutputSpec{
				Name:  "ranch status",
				Type:  OutputTypeMaintainedDocument,
				Ref:   "core:ranch.md",
				Tiers: []OutputTier{OutputTierStatusLine, OutputTierTeaser, OutputTierDigest},
			},
			wantTool: "replace_output_ranch_status",
		},
		{
			name: "status line alone anchors the ladder",
			output: OutputSpec{
				Name:  "ranch status",
				Type:  OutputTypeMaintainedDocument,
				Ref:   "core:ranch.md",
				Tiers: []OutputTier{OutputTierStatusLine},
			},
			wantTool: "replace_output_ranch_status",
		},
		{
			name: "tiers without status line rejected",
			output: OutputSpec{
				Name:  "ranch status",
				Type:  OutputTypeMaintainedDocument,
				Ref:   "core:ranch.md",
				Tiers: []OutputTier{OutputTierTeaser, OutputTierDigest},
			},
			wantErr: true,
		},
		{
			name: "unknown tier rejected",
			output: OutputSpec{
				Name:  "ranch status",
				Type:  OutputTypeMaintainedDocument,
				Ref:   "core:ranch.md",
				Tiers: []OutputTier{OutputTierStatusLine, OutputTier("hud")},
			},
			wantErr: true,
		},
		{
			name: "duplicate tier rejected",
			output: OutputSpec{
				Name:  "ranch status",
				Type:  OutputTypeMaintainedDocument,
				Ref:   "core:ranch.md",
				Tiers: []OutputTier{OutputTierStatusLine, OutputTierStatusLine},
			},
			wantErr: true,
		},
		{
			name: "tiers on journal rejected",
			output: OutputSpec{
				Name:  "service journal",
				Type:  OutputTypeJournalDocument,
				Ref:   "core:service-journal.md",
				Tiers: []OutputTier{OutputTierStatusLine},
			},
			wantErr: true,
		},
		{
			name: "tiers on working notes rejected",
			output: OutputSpec{
				Name:  "ranch notes",
				Type:  OutputTypeWorkingNotes,
				Ref:   "core:ranch-notes.md",
				Tiers: []OutputTier{OutputTierStatusLine},
			},
			wantErr: true,
		},
		{
			name: "tiers on internal maintained document rejected",
			output: OutputSpec{
				Name:     "hypotheses",
				Type:     OutputTypeMaintainedDocument,
				Ref:      "core:hypotheses.md",
				Audience: OutputAudienceInternal,
				Tiers:    []OutputTier{OutputTierStatusLine},
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
			name:   "journal defaults published",
			output: OutputSpec{Type: OutputTypeJournalDocument},
			want:   OutputAudiencePublished,
		},
		{
			name:   "working notes default internal",
			output: OutputSpec{Type: OutputTypeWorkingNotes},
			want:   OutputAudienceInternal,
		},
		{
			name:   "explicit audience wins",
			output: OutputSpec{Type: OutputTypeJournalDocument, Audience: OutputAudienceInternal},
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

func TestCloneOutputsDeepCopiesTiers(t *testing.T) {
	src := []OutputSpec{{
		Name:  "ranch status",
		Type:  OutputTypeMaintainedDocument,
		Ref:   "core:ranch.md",
		Tiers: []OutputTier{OutputTierStatusLine, OutputTierTeaser},
	}}
	dst := cloneOutputs(src)
	dst[0].Tiers[1] = OutputTierDigest
	if src[0].Tiers[1] != OutputTierTeaser {
		t.Fatalf("cloneOutputs shares Tiers backing array: src mutated to %q", src[0].Tiers[1])
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
				Tiers:   []OutputTier{OutputTierStatusLine, OutputTierTeaser},
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
	if !strings.Contains(string(data), `"tiers"`) {
		t.Fatalf("marshaled spec missing tiers: %s", string(data))
	}

	var got Spec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Outputs) != 2 {
		t.Fatalf("Outputs len = %d, want 2", len(got.Outputs))
	}
	if got.Outputs[0].ToolName() != "replace_output_status" {
		t.Fatalf("output tool = %q, want replace_output_status", got.Outputs[0].ToolName())
	}
	if len(got.Outputs[0].Tiers) != 2 || got.Outputs[0].Tiers[0] != OutputTierStatusLine {
		t.Fatalf("round-tripped tiers = %v, want [status_line teaser]", got.Outputs[0].Tiers)
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
