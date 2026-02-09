package llm

import (
	"testing"
)

func TestParseTextToolCalls(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		validTools []string
		wantCount  int
		wantName   string // First tool name if wantCount > 0
	}{
		{
			name:      "empty content",
			content:   "",
			wantCount: 0,
		},
		{
			name:      "whitespace only",
			content:   "   \n\t  ",
			wantCount: 0,
		},
		{
			name:      "plain text no JSON",
			content:   "The sun is currently up.",
			wantCount: 0,
		},
		{
			name:      "single tool call object",
			content:   `{"name": "get_state", "arguments": {"entity_id": "sun.sun"}}`,
			wantCount: 1,
			wantName:  "get_state",
		},
		{
			name:      "single tool call with whitespace",
			content:   `  {"name": "get_state", "arguments": {"entity_id": "sun.sun"}}  `,
			wantCount: 1,
			wantName:  "get_state",
		},
		{
			name:      "array of tool calls",
			content:   `[{"name": "get_state", "arguments": {"entity_id": "sun.sun"}}, {"name": "list_entities", "arguments": {}}]`,
			wantCount: 2,
			wantName:  "get_state",
		},
		{
			name:      "tagged tool call",
			content:   `<tool_call>{"name": "call_service", "arguments": {"domain": "light", "service": "turn_on"}}</tool_call>`,
			wantCount: 1,
			wantName:  "call_service",
		},
		{
			name:      "tagged tool call without closing tag",
			content:   `<tool_call>{"name": "get_state", "arguments": {"entity_id": "light.kitchen"}}`,
			wantCount: 1,
			wantName:  "get_state",
		},
		{
			name:      "tagged with preamble",
			content:   `Let me check that for you. <tool_call>{"name": "get_state", "arguments": {"entity_id": "sun.sun"}}</tool_call>`,
			wantCount: 1,
			wantName:  "get_state",
		},
		{
			name:      "empty arguments",
			content:   `{"name": "list_entities", "arguments": {}}`,
			wantCount: 1,
			wantName:  "list_entities",
		},
		{
			name:      "nested arguments",
			content:   `{"name": "call_service", "arguments": {"domain": "light", "service": "turn_on", "data": {"brightness": 255}}}`,
			wantCount: 1,
			wantName:  "call_service",
		},
		{
			name:      "malformed JSON",
			content:   `{"name": "get_state", "arguments": {`,
			wantCount: 0,
		},
		{
			name:      "JSON without name field",
			content:   `{"foo": "bar", "arguments": {}}`,
			wantCount: 0,
		},
		{
			name:      "JSON with empty name",
			content:   `{"name": "", "arguments": {}}`,
			wantCount: 0,
		},
		// Validation tests
		{
			name:       "valid tool with validation",
			content:    `{"name": "get_state", "arguments": {"entity_id": "sun.sun"}}`,
			validTools: []string{"get_state", "call_service"},
			wantCount:  1,
			wantName:   "get_state",
		},
		{
			name:       "invalid tool rejected by validation",
			content:    `{"name": "hack_the_planet", "arguments": {}}`,
			validTools: []string{"get_state", "call_service"},
			wantCount:  0,
		},
		{
			name:       "mixed valid/invalid in array",
			content:    `[{"name": "get_state", "arguments": {}}, {"name": "invalid_tool", "arguments": {}}]`,
			validTools: []string{"get_state", "call_service"},
			wantCount:  1,
			wantName:   "get_state",
		},
		{
			name:       "no validation (nil validTools)",
			content:    `{"name": "any_tool_name", "arguments": {}}`,
			validTools: nil,
			wantCount:  1,
			wantName:   "any_tool_name",
		},
		{
			name:       "no validation (empty validTools)",
			content:    `{"name": "any_tool_name", "arguments": {}}`,
			validTools: []string{},
			wantCount:  1,
			wantName:   "any_tool_name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTextToolCalls(tt.content, tt.validTools)

			if len(got) != tt.wantCount {
				t.Errorf("parseTextToolCalls() returned %d tools, want %d", len(got), tt.wantCount)
				return
			}

			if tt.wantCount > 0 && got[0].Function.Name != tt.wantName {
				t.Errorf("parseTextToolCalls() first tool name = %q, want %q", got[0].Function.Name, tt.wantName)
			}
		})
	}
}

func TestExtractToolNames(t *testing.T) {
	tests := []struct {
		name  string
		tools []map[string]any
		want  []string
	}{
		{
			name:  "nil tools",
			tools: nil,
			want:  nil,
		},
		{
			name:  "empty tools",
			tools: []map[string]any{},
			want:  nil,
		},
		{
			name: "single tool",
			tools: []map[string]any{
				{"function": map[string]any{"name": "get_state", "description": "Gets entity state"}},
			},
			want: []string{"get_state"},
		},
		{
			name: "multiple tools",
			tools: []map[string]any{
				{"function": map[string]any{"name": "get_state"}},
				{"function": map[string]any{"name": "call_service"}},
				{"function": map[string]any{"name": "list_entities"}},
			},
			want: []string{"get_state", "call_service", "list_entities"},
		},
		{
			name: "malformed tool (no function)",
			tools: []map[string]any{
				{"name": "orphan_name"},
			},
			want: []string{},
		},
		{
			name: "mixed valid and malformed",
			tools: []map[string]any{
				{"function": map[string]any{"name": "valid_tool"}},
				{"broken": "entry"},
				{"function": map[string]any{"name": "another_valid"}},
			},
			want: []string{"valid_tool", "another_valid"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToolNames(tt.tools)
			if len(got) != len(tt.want) {
				t.Errorf("extractToolNames() = %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("extractToolNames()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseTextToolCalls_Arguments(t *testing.T) {
	content := `{"name": "call_service", "arguments": {"domain": "light", "service": "turn_on", "entity_id": "light.kitchen"}}`

	calls := parseTextToolCalls(content, nil)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}

	args := calls[0].Function.Arguments
	if args["domain"] != "light" {
		t.Errorf("domain = %v, want 'light'", args["domain"])
	}
	if args["service"] != "turn_on" {
		t.Errorf("service = %v, want 'turn_on'", args["service"])
	}
	if args["entity_id"] != "light.kitchen" {
		t.Errorf("entity_id = %v, want 'light.kitchen'", args["entity_id"])
	}
}
