package modeleval

import (
	"errors"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
)

func TestCaseFromModelCall(t *testing.T) {
	t.Parallel()

	call := logging.ModelCallDetail{
		Iteration: 2,
		Model:     "reference",
		Messages: []logging.MessageDetail{
			{Index: 0, Role: "system", Content: "system"},
			{Index: 1, Role: "user", Content: "find it"},
		},
		Tools: []map[string]any{{"type": "function", "function": map[string]any{"name": "search"}}},
		Response: logging.MessageDetail{
			Role: "assistant",
			ToolCalls: []logging.MessageToolCallDetail{{
				ID: "call_ref", Name: "search", Arguments: `{"query":"it"}`,
			}},
		},
	}
	case_, err := CaseFromModelCall("r_1", call)
	if err != nil {
		t.Fatal(err)
	}
	if case_.ID != "r_1/2" || case_.Expected.ToolCalls[0].Function.Name != "search" {
		t.Fatalf("case = %#v", case_)
	}
	if case_.Expected.ToolCalls[0].Function.Arguments["query"] != "it" {
		t.Fatalf("arguments = %#v", case_.Expected.ToolCalls[0].Function.Arguments)
	}
}

func TestCaseFromModelCallRejectsIncompleteInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		detail logging.MessageDetail
		want   error
	}{
		{name: "content truncated", detail: logging.MessageDetail{Role: "user", ContentTruncated: true}, want: ErrTruncatedInput},
		{name: "arguments truncated", detail: logging.MessageDetail{Role: "assistant", ToolCalls: []logging.MessageToolCallDetail{{Name: "x", ArgumentsTruncated: true}}}, want: ErrTruncatedInput},
		{name: "image omitted", detail: logging.MessageDetail{Role: "user", Images: []logging.MessageImageDetail{{MediaType: "image/png"}}}, want: ErrImageInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := CaseFromModelCall("r", logging.ModelCallDetail{
				Messages: []logging.MessageDetail{tt.detail},
				Response: logging.MessageDetail{Role: "assistant", Content: "done"},
			})
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestSnapshotValidate(t *testing.T) {
	t.Parallel()

	valid := Snapshot{
		SchemaVersion: SnapshotSchemaVersion,
		Cases:         []Case{{ID: "r/0", Messages: []llm.Message{{Role: "user", Content: "hi"}}}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.Cases = append(valid.Cases, valid.Cases[0])
	if err := valid.Validate(); err == nil {
		t.Fatal("duplicate case ID passed validation")
	}
}
