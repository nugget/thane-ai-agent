package llm

import (
	"strings"
	"testing"
)

func TestParseTextToolCalls_FencedToolBlock(t *testing.T) {
	profile := DefaultToolCallTextProfile()
	content := "```tool\n{\"name\":\"tag_activate\",\"arguments\":{\"tag\":\"forge\"}}\n```"

	calls := ParseTextToolCalls(content, []string{"tag_activate", "tag_deactivate"}, profile)
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	if calls[0].Function.Name != "tag_activate" {
		t.Fatalf("tool name = %q, want tag_activate", calls[0].Function.Name)
	}
	if got := calls[0].Function.Arguments["tag"]; got != "forge" {
		t.Fatalf("tag = %v, want forge", got)
	}
}

func TestApplyTextToolCallFallback_PreservesUnknownToolShapeForRuntimeRepair(t *testing.T) {
	profile := DefaultToolCallTextProfile()
	resp := &ChatResponse{
		Message: Message{
			Content: "```tool\n{\"name\":\"request_capability\",\"arguments\":{\"tag\":\"forge\"}}\n```",
		},
	}

	ApplyTextToolCallFallback(resp, []string{"tag_activate", "tag_deactivate"}, profile)

	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("len(tool_calls) = %d, want 1", len(resp.Message.ToolCalls))
	}
	if resp.Message.ToolCalls[0].Function.Name != "request_capability" {
		t.Fatalf("tool name = %q, want request_capability", resp.Message.ToolCalls[0].Function.Name)
	}
	if resp.Message.Content != "" {
		t.Fatalf("content = %q, want empty after promoting repairable tool shape", resp.Message.Content)
	}
}

func TestStripTrailingToolCallText_StripsTrailingFencedToolBlock(t *testing.T) {
	profile := DefaultToolCallTextProfile()
	content := "I can do that for you.\n```tool\n{\"name\":\"tag_activate\",\"arguments\":{\"tag\":\"forge\"}}\n```"

	cleaned := StripTrailingToolCallText(content, []string{"tag_activate"}, profile)
	if cleaned != "I can do that for you." {
		t.Fatalf("cleaned = %q, want prose without trailing tool block", cleaned)
	}
}

func TestApplyTextToolCallFallback_ReportsAction(t *testing.T) {
	t.Parallel()

	offered := []string{"tag_activate", "tag_deactivate"}
	native := ToolCall{ID: "call_native"}
	native.Function.Name = "tag_activate"
	native.Function.Arguments = map[string]any{"tag": "forge"}

	tests := []struct {
		name        string
		offered     []string
		message     Message
		want        TextToolAction
		wantCalls   []string
		wantContent string
	}{
		{
			name:      "structured calls are left alone",
			offered:   offered,
			message:   Message{Content: "Loading it now.", ToolCalls: []ToolCall{native}},
			want:      TextToolActionNone,
			wantCalls: []string{"tag_activate"}, wantContent: "Loading it now.",
		},
		{
			name:    "empty content",
			offered: offered,
			message: Message{},
			want:    TextToolActionNone,
		},
		{
			name:        "plain prose",
			offered:     offered,
			message:     Message{Content: "All four lights are off."},
			want:        TextToolActionNone,
			wantContent: "All four lights are off.",
		},
		{
			name:      "whole-text json",
			offered:   offered,
			message:   Message{Content: `{"name":"tag_activate","arguments":{"tag":"forge"}}`},
			want:      TextToolActionRecovered,
			wantCalls: []string{"tag_activate"},
		},
		{
			name:      "fenced",
			offered:   offered,
			message:   Message{Content: "```json\n{\"name\":\"tag_activate\",\"arguments\":{\"tag\":\"forge\"}}\n```"},
			want:      TextToolActionRecovered,
			wantCalls: []string{"tag_activate"},
		},
		{
			name:      "tagged",
			offered:   offered,
			message:   Message{Content: "<tool_call>\n{\"name\":\"tag_deactivate\",\"arguments\":{\"tag\":\"forge\"}}\n</tool_call>"},
			want:      TextToolActionRecovered,
			wantCalls: []string{"tag_deactivate"},
		},
		{
			name:      "any name recovers when nothing was offered",
			message:   Message{Content: `{"name":"tag_activate","arguments":{}}`},
			want:      TextToolActionRecovered,
			wantCalls: []string{"tag_activate"},
		},
		{
			name:      "unoffered name",
			offered:   offered,
			message:   Message{Content: `{"name":"request_capability","arguments":{"tag":"forge"}}`},
			want:      TextToolActionRepairRecoveredUnoffered,
			wantCalls: []string{"request_capability"},
		},
		{
			name:    "stringified arguments",
			offered: offered,
			message: Message{Content: `{"name":"tag_activate","arguments":"{\"tag\":\"forge\"}"}`},
			want:    TextToolActionSuppressed,
		},
		{
			name:        "fenced call after prose",
			offered:     offered,
			message:     Message{Content: "Loading forge now.\n```tool\n{\"name\":\"tag_activate\",\"arguments\":{\"tag\":\"forge\"}}\n```"},
			want:        TextToolActionStrippedTrailing,
			wantContent: "Loading forge now.",
		},
		{
			name:        "bare argument-free json call after prose",
			offered:     offered,
			message:     Message{Content: `Resetting tags now. {"name":"tag_deactivate"}`},
			want:        TextToolActionStrippedTrailing,
			wantContent: "Resetting tags now.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp := &ChatResponse{Message: tt.message}
			got := ApplyTextToolCallFallback(resp, tt.offered, DefaultToolCallTextProfile())
			if got != tt.want {
				t.Fatalf("action = %q, want %q", got, tt.want)
			}
			if resp.TextToolAction != tt.want {
				t.Fatalf("resp.TextToolAction = %q, want %q", resp.TextToolAction, tt.want)
			}
			var names []string
			for _, call := range resp.Message.ToolCalls {
				names = append(names, call.Function.Name)
			}
			if strings.Join(names, ",") != strings.Join(tt.wantCalls, ",") {
				t.Fatalf("tool calls = %v, want %v", names, tt.wantCalls)
			}
			if resp.Message.Content != tt.wantContent {
				t.Fatalf("content = %q, want %q", resp.Message.Content, tt.wantContent)
			}
		})
	}
}

func TestApplyTextToolCallFallback_NilResponse(t *testing.T) {
	t.Parallel()
	if got := ApplyTextToolCallFallback(nil, nil, DefaultToolCallTextProfile()); got != TextToolActionNone {
		t.Fatalf("action = %q, want %q", got, TextToolActionNone)
	}
}
