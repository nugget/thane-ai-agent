package llm

import "testing"

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
