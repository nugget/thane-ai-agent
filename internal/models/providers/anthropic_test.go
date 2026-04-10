package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/llm"
)

func TestConvertToAnthropic(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello!"},
		{Role: "assistant", Content: "Hi there!"},
		{Role: "user", Content: "Turn on the lights."},
	}

	result, system := convertToAnthropic(messages)

	if system != "You are a helpful assistant." {
		t.Errorf("expected system prompt extracted, got %q", system)
	}

	if len(result) != 3 {
		t.Fatalf("expected 3 messages (no system), got %d", len(result))
	}

	if result[0].Role != "user" {
		t.Errorf("expected first message to be user, got %s", result[0].Role)
	}
}

func TestConvertToAnthropicWithToolCalls(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "You are a home assistant."},
		{Role: "user", Content: "Turn on lights."},
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "toolu_abc123",
				Function: struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				}{
					Name:      "control_device",
					Arguments: map[string]any{"entity": "light.kitchen"},
				},
			}},
		},
		{Role: "tool", Content: "Done.", ToolCallID: "toolu_abc123"},
	}

	result, system := convertToAnthropic(messages)

	if system != "You are a home assistant." {
		t.Errorf("unexpected system: %q", system)
	}

	if len(result) != 3 { // user, assistant with tool_use, user with tool_result
		t.Fatalf("expected 3 messages, got %d", len(result))
	}

	// Check assistant message has tool_use blocks
	assistantContent, ok := result[1].Content.([]anthropicContent)
	if !ok {
		t.Fatal("expected assistant content to be []anthropicContent")
	}
	if len(assistantContent) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(assistantContent))
	}
	if assistantContent[0].Type != "tool_use" {
		t.Errorf("expected tool_use block, got %s", assistantContent[0].Type)
	}
	if assistantContent[0].ID != "toolu_abc123" {
		t.Errorf("expected tool_use ID toolu_abc123, got %s", assistantContent[0].ID)
	}

	// Check tool result
	toolResultContent, ok := result[2].Content.([]anthropicContent)
	if !ok {
		t.Fatal("expected tool result content to be []anthropicContent")
	}
	if toolResultContent[0].Type != "tool_result" {
		t.Errorf("expected tool_result, got %s", toolResultContent[0].Type)
	}
	if toolResultContent[0].ToolUseID != "toolu_abc123" {
		t.Errorf("expected tool_use_id toolu_abc123, got %s", toolResultContent[0].ToolUseID)
	}
}

func TestConvertToolsToAnthropic(t *testing.T) {
	tools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "get_state",
				"description": "Get entity state",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"entity_id": map[string]any{
							"type":        "string",
							"description": "The entity ID",
						},
					},
					"required": []string{"entity_id"},
				},
			},
		},
	}

	result := convertToolsToAnthropic(tools)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	if result[0].Name != "get_state" {
		t.Errorf("expected tool name get_state, got %s", result[0].Name)
	}
	if result[0].Description != "Get entity state" {
		t.Errorf("expected description, got %s", result[0].Description)
	}
	if result[0].CacheControl == nil || result[0].CacheControl.TTL != "1h" {
		t.Fatalf("expected last tool to carry 1h cache control, got %+v", result[0].CacheControl)
	}
}

func TestConvertToolsToAnthropic_StripsTopLevelCompositionKeywords(t *testing.T) {
	tools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "notify_loop",
				"description": "Notify a loop",
				"parameters": map[string]any{
					"type": "object",
					"anyOf": []any{
						map[string]any{"required": []any{"loop_id"}},
						map[string]any{"required": []any{"name"}},
					},
					"properties": map[string]any{
						"loop_id": map[string]any{"type": "string"},
						"name":    map[string]any{"type": "string"},
						"duration": map[string]any{
							"anyOf": []any{
								map[string]any{"type": "string"},
								map[string]any{"type": "number"},
							},
						},
					},
				},
			},
		},
	}

	result := convertToolsToAnthropic(tools)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	schema, ok := result[0].InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("InputSchema type = %T, want map[string]any", result[0].InputSchema)
	}
	if _, ok := schema["anyOf"]; ok {
		t.Fatalf("top-level anyOf should be removed for Anthropic: %#v", schema)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", schema["properties"])
	}
	if _, ok := props["loop_id"]; !ok {
		t.Fatalf("loop_id missing from sanitized schema: %#v", props)
	}
	duration, ok := props["duration"].(map[string]any)
	if !ok {
		t.Fatalf("duration type = %T, want map[string]any", props["duration"])
	}
	if _, ok := duration["anyOf"]; !ok {
		t.Fatalf("nested anyOf should be preserved: %#v", duration)
	}
}

func TestConvertFromAnthropic(t *testing.T) {
	resp := &anthropicResponse{
		Model: "claude-opus-4-20250514",
		Role:  "assistant",
		Content: []anthropicContent{
			{Type: "text", Text: "I'll check that for you."},
			{
				Type:  "tool_use",
				ID:    "toolu_xyz789",
				Name:  "get_state",
				Input: map[string]any{"entity_id": "sun.sun"},
			},
		},
		StopReason: "tool_use",
	}

	result := convertFromAnthropic(resp)

	if result.Message.Content != "I'll check that for you." {
		t.Errorf("unexpected content: %q", result.Message.Content)
	}
	if len(result.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.Message.ToolCalls))
	}
	if result.Message.ToolCalls[0].ID != "toolu_xyz789" {
		t.Errorf("expected tool call ID toolu_xyz789, got %s", result.Message.ToolCalls[0].ID)
	}
	if result.Message.ToolCalls[0].Function.Name != "get_state" {
		t.Errorf("expected get_state, got %s", result.Message.ToolCalls[0].Function.Name)
	}
}

func TestAnthropicClientImplementsInterface(t *testing.T) {
	// Compile-time check that AnthropicClient implements Client
	var _ llm.Client = (*AnthropicClient)(nil)
}

func TestOllamaClientImplementsInterface(t *testing.T) {
	// Compile-time check that OllamaClient implements Client
	var _ llm.Client = (*OllamaClient)(nil)
}

func TestAnthropicRequestSerialization(t *testing.T) {
	req := anthropicRequest{
		Model:     "claude-opus-4-20250514",
		Messages:  []anthropicMessage{{Role: "user", Content: "test"}},
		System:    "You are helpful.",
		MaxTokens: 4096,
		Tools: []anthropicTool{{
			Name:        "test_tool",
			Description: "A test tool",
			InputSchema: map[string]any{"type": "object"},
		}},
		CacheControl: &anthropicCacheControl{Type: "ephemeral"},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	// Verify it deserializes back
	var decoded anthropicRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Model != req.Model {
		t.Errorf("model mismatch: %s vs %s", decoded.Model, req.Model)
	}
	decodedSystem, ok := decoded.System.(string)
	if !ok {
		t.Fatalf("decoded.System type = %T, want string", decoded.System)
	}
	if decodedSystem != req.System {
		t.Errorf("system mismatch: %s vs %s", decodedSystem, req.System)
	}
	if decoded.CacheControl == nil || decoded.CacheControl.Type != "ephemeral" {
		t.Fatalf("cache_control = %+v, want ephemeral", decoded.CacheControl)
	}
}

func TestAnthropicSystemBlocks_ApplyCacheBreakpoints(t *testing.T) {
	sections := []llm.PromptSection{
		{Name: "PERSONA", Content: "PERSONA", CacheTTL: "1h"},
		{Name: "RUNTIME CONTRACT", Content: "RUNTIME", CacheTTL: "1h"},
		{Name: "TALENTS TAGGED", Content: "TAGGED", CacheTTL: "5m"},
		{Name: "CURRENT CONDITIONS", Content: "NOW"},
	}

	blocks := anthropicSystemBlocks(sections)
	if len(blocks) != 4 {
		t.Fatalf("len(blocks) = %d, want 4", len(blocks))
	}
	if blocks[0].CacheControl != nil {
		t.Fatalf("first 1h block should not carry the breakpoint: %+v", blocks[0].CacheControl)
	}
	if blocks[1].CacheControl == nil || blocks[1].CacheControl.TTL != "1h" {
		t.Fatalf("second block should close the 1h cache run: %+v", blocks[1].CacheControl)
	}
	if blocks[2].CacheControl == nil || blocks[2].CacheControl.TTL != "5m" {
		t.Fatalf("tagged block should close the 5m cache run: %+v", blocks[2].CacheControl)
	}
	if blocks[3].CacheControl != nil {
		t.Fatalf("uncached tail should not carry cache control: %+v", blocks[3].CacheControl)
	}
}

func TestAnthropicSystemPayload_UsesPromptSectionsWhenPresent(t *testing.T) {
	messages := []llm.Message{
		{
			Role:    "system",
			Content: "opaque fallback",
			Sections: []llm.PromptSection{
				{Name: "PERSONA", Content: "PERSONA", CacheTTL: "1h"},
				{Name: "CURRENT CONDITIONS", Content: "NOW"},
			},
		},
	}

	payload := anthropicSystemPayload(messages, "opaque fallback")
	blocks, ok := payload.([]anthropicContent)
	if !ok {
		t.Fatalf("payload type = %T, want []anthropicContent", payload)
	}
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(blocks))
	}
	if blocks[0].CacheControl == nil || blocks[0].CacheControl.TTL != "1h" {
		t.Fatalf("expected cached persona block, got %+v", blocks[0].CacheControl)
	}
}

func TestAnthropicPromptCacheControl_NotSuppressedByToolCacheBreakpoints(t *testing.T) {
	tools := convertToolsToAnthropic([]map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "get_state",
				"description": "Get entity state",
				"parameters":  map[string]any{"type": "object"},
			},
		},
	})
	if len(tools) != 1 || tools[0].CacheControl == nil {
		t.Fatalf("expected tool cache control, got %+v", tools)
	}

	systemPayload := anthropicSystemPayload([]llm.Message{{Role: "system", Content: "You are a helpful assistant."}}, "You are a helpful assistant.")
	explicit := anthropicUsesExplicitPromptCaching(systemPayload)
	if explicit {
		t.Fatal("plain-string system prompt should not count as explicit prompt caching")
	}

	ctrl := anthropicPromptCacheControl("You are a helpful assistant.", []anthropicMessage{{Role: "user", Content: "check"}}, tools, explicit)
	if ctrl == nil || ctrl.Type != "ephemeral" {
		t.Fatalf("request-level cache_control = %+v, want ephemeral", ctrl)
	}
}

func TestAnthropicPromptCacheControl_SuppressedBySectionCacheBreakpoints(t *testing.T) {
	systemPayload := anthropicSystemPayload([]llm.Message{
		{
			Role:    "system",
			Content: "fallback",
			Sections: []llm.PromptSection{
				{Name: "PERSONA", Content: "PERSONA", CacheTTL: "1h"},
			},
		},
	}, "fallback")

	explicit := anthropicUsesExplicitPromptCaching(systemPayload)
	if !explicit {
		t.Fatal("sectioned system prompt should count as explicit prompt caching")
	}

	ctrl := anthropicPromptCacheControl("fallback", []anthropicMessage{{Role: "user", Content: "check"}}, nil, explicit)
	if ctrl != nil {
		t.Fatalf("request-level cache_control = %+v, want nil when explicit system block caching is present", ctrl)
	}
}

func TestShouldUseAnthropicPromptCaching(t *testing.T) {
	tests := []struct {
		name   string
		system string
		msgs   []anthropicMessage
		tools  []anthropicTool
		want   bool
	}{
		{
			name:   "tool loop enables caching",
			system: "You are a tool-using assistant.",
			msgs:   []anthropicMessage{{Role: "user", Content: "check the state"}},
			tools:  []anthropicTool{{Name: "get_state", InputSchema: map[string]any{"type": "object"}}},
			want:   true,
		},
		{
			name:   "multi turn conversation enables caching",
			system: "You are a helpful assistant.",
			msgs: []anthropicMessage{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi"},
				{Role: "user", Content: "what did I say?"},
			},
			want: true,
		},
		{
			name:   "large system prompt enables caching",
			system: strings.Repeat("x", 4096),
			msgs:   []anthropicMessage{{Role: "user", Content: "summarize this"}},
			want:   true,
		},
		{
			name:   "simple one shot request stays uncached",
			system: "You are a helpful assistant.",
			msgs:   []anthropicMessage{{Role: "user", Content: "hello"}},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldUseAnthropicPromptCaching(tt.system, tt.msgs, tt.tools)
			if got != tt.want {
				t.Fatalf("shouldUseAnthropicPromptCaching() = %v, want %v", got, tt.want)
			}
		})
	}
}
