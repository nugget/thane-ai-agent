package llm

import (
	"testing"
)

func TestToOllamaMessages(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hello"},
		{
			Role:    "user",
			Content: "describe this",
			Images: []ImageContent{
				{Data: "base64data", MediaType: "image/jpeg"},
				{Data: "moredata", MediaType: "image/png"},
			},
		},
		{
			Role:       "tool",
			Content:    `{"result": "ok"}`,
			ToolCallID: "call_123",
		},
		{
			Role:    "assistant",
			Content: "",
			ToolCalls: []ToolCall{
				{ID: "call_123", Function: struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				}{Name: "get_state"}},
			},
		},
	}

	out := toOllamaMessages(msgs)
	if len(out) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(out))
	}

	// Plain message — no images, no tool fields.
	if out[0].Role != "user" || out[0].Content != "hello" {
		t.Errorf("msg[0] = %+v", out[0])
	}
	if len(out[0].Images) != 0 {
		t.Errorf("msg[0] should have no images")
	}

	// Image message — images extracted as base64 strings.
	if len(out[1].Images) != 2 {
		t.Fatalf("msg[1] images = %d, want 2", len(out[1].Images))
	}
	if out[1].Images[0] != "base64data" || out[1].Images[1] != "moredata" {
		t.Errorf("msg[1] images = %v", out[1].Images)
	}

	// Tool result — ToolCallID preserved.
	if out[2].ToolCallID != "call_123" {
		t.Errorf("msg[2] tool_call_id = %q, want call_123", out[2].ToolCallID)
	}

	// Assistant with tool calls.
	if len(out[3].ToolCalls) != 1 || out[3].ToolCalls[0].Function.Name != "get_state" {
		t.Errorf("msg[3] tool_calls = %+v", out[3].ToolCalls)
	}
}

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

func TestParseTextToolCalls_ConcatenatedJSON(t *testing.T) {
	// Test concatenated JSON objects (qwen-style): {...}{...}{...}
	content := `{"name": "archive_search", "arguments": {"query": "model thane:local routing"}}{"name": "archive_search", "arguments": {"query": "what was discussed previously"}}{"name": "file_read", "arguments": {"path": "logs/log.txt"}}`
	validTools := []string{"archive_search", "file_read", "archive_sessions"}

	calls := parseTextToolCalls(content, validTools)
	if len(calls) != 3 {
		t.Fatalf("expected 3 tool calls, got %d", len(calls))
	}

	if calls[0].Function.Name != "archive_search" {
		t.Errorf("call[0] name = %q, want archive_search", calls[0].Function.Name)
	}
	if calls[1].Function.Name != "archive_search" {
		t.Errorf("call[1] name = %q, want archive_search", calls[1].Function.Name)
	}
	if calls[2].Function.Name != "file_read" {
		t.Errorf("call[2] name = %q, want file_read", calls[2].Function.Name)
	}
	if calls[2].Function.Arguments["path"] != "logs/log.txt" {
		t.Errorf("call[2] path = %v, want logs/log.txt", calls[2].Function.Arguments["path"])
	}
}

func TestParseTextToolCalls_ConcatenatedWithTrailingText(t *testing.T) {
	// Concatenated JSON followed by prose (as seen from qwen)
	content := `{"name": "archive_search", "arguments": {"query": "routing"}}{"name": "file_read", "arguments": {"path": "log.txt"}}Session Management in Open WebUI and Thane`
	validTools := []string{"archive_search", "file_read"}

	calls := parseTextToolCalls(content, validTools)
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d (trailing text should be ignored)", len(calls))
	}
}

func TestParseTextToolCalls_ToolNameSpaceJSON(t *testing.T) {
	// Test "tool_name {json}" format that some models output
	tests := []struct {
		name       string
		content    string
		validTools []string
		wantTool   string
		wantArgs   map[string]any
	}{
		{
			name:       "find_entity format",
			content:    `find_entity {"description": "access point LED", "area": "office", "domain": "light"}`,
			validTools: []string{"find_entity", "call_service"},
			wantTool:   "find_entity",
			wantArgs:   map[string]any{"description": "access point LED", "area": "office", "domain": "light"},
		},
		{
			name:       "call_service format",
			content:    `call_service {"domain": "light", "service": "turn_on"}`,
			validTools: []string{"find_entity", "call_service"},
			wantTool:   "call_service",
			wantArgs:   map[string]any{"domain": "light", "service": "turn_on"},
		},
		{
			name:       "with trailing text",
			content:    `find_entity {"description": "office light"} I will turn it on.`,
			validTools: []string{"find_entity"},
			wantTool:   "find_entity",
			wantArgs:   map[string]any{"description": "office light"},
		},
		{
			name:       "invalid tool ignored",
			content:    `unknown_tool {"foo": "bar"}`,
			validTools: []string{"find_entity"},
			wantTool:   "",
			wantArgs:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := parseTextToolCalls(tt.content, tt.validTools)

			if tt.wantTool == "" {
				if len(calls) != 0 {
					t.Errorf("expected no tool calls, got %d", len(calls))
				}
				return
			}

			if len(calls) != 1 {
				t.Fatalf("expected 1 tool call, got %d", len(calls))
			}

			if calls[0].Function.Name != tt.wantTool {
				t.Errorf("tool name = %q, want %q", calls[0].Function.Name, tt.wantTool)
			}

			for k, want := range tt.wantArgs {
				got := calls[0].Function.Arguments[k]
				if got != want {
					t.Errorf("args[%q] = %v, want %v", k, got, want)
				}
			}
		})
	}
}
