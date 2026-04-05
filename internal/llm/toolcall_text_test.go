package llm

import "testing"

func TestParseTextToolCalls_FencedToolBlock(t *testing.T) {
	profile := DefaultToolCallTextProfile()
	content := "```tool\n{\"name\":\"activate_capability\",\"arguments\":{\"tag\":\"forge\"}}\n```"

	calls := ParseTextToolCalls(content, []string{"activate_capability", "deactivate_capability"}, profile)
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	if calls[0].Function.Name != "activate_capability" {
		t.Fatalf("tool name = %q, want activate_capability", calls[0].Function.Name)
	}
	if got := calls[0].Function.Arguments["tag"]; got != "forge" {
		t.Fatalf("tag = %v, want forge", got)
	}
}

func TestApplyTextToolCallFallback_SuppressesInvalidFencedToolShape(t *testing.T) {
	profile := DefaultToolCallTextProfile()
	resp := &ChatResponse{
		Message: Message{
			Content: "```tool\n{\"name\":\"list_capabilities\",\"arguments\":{}}\n```",
		},
	}

	ApplyTextToolCallFallback(resp, []string{"activate_capability", "deactivate_capability"}, profile)

	if len(resp.Message.ToolCalls) != 0 {
		t.Fatalf("len(tool_calls) = %d, want 0", len(resp.Message.ToolCalls))
	}
	if resp.Message.Content != "" {
		t.Fatalf("content = %q, want empty after suppressing hallucinated tool shape", resp.Message.Content)
	}
}

func TestStripTrailingToolCallText_StripsTrailingFencedToolBlock(t *testing.T) {
	profile := DefaultToolCallTextProfile()
	content := "I can do that for you.\n```tool\n{\"name\":\"activate_capability\",\"arguments\":{\"tag\":\"forge\"}}\n```"

	cleaned := StripTrailingToolCallText(content, []string{"activate_capability"}, profile)
	if cleaned != "I can do that for you." {
		t.Fatalf("cleaned = %q, want prose without trailing tool block", cleaned)
	}
}
