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

	var got Spec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Outputs) != 1 {
		t.Fatalf("Outputs len = %d, want 1", len(got.Outputs))
	}
	if got.Outputs[0].ToolName() != "replace_output_status" {
		t.Fatalf("output tool = %q, want replace_output_status", got.Outputs[0].ToolName())
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
