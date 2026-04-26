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
