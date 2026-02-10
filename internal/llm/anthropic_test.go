package llm

import (
	"encoding/json"
	"testing"
)

func TestConvertToAnthropic(t *testing.T) {
	messages := []Message{
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
	messages := []Message{
		{Role: "system", Content: "You are a home assistant."},
		{Role: "user", Content: "Turn on lights."},
		{
			Role: "assistant",
			ToolCalls: []ToolCall{{
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
	var _ Client = (*AnthropicClient)(nil)
}

func TestOllamaClientImplementsInterface(t *testing.T) {
	// Compile-time check that OllamaClient implements Client
	var _ Client = (*OllamaClient)(nil)
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
	if decoded.System != req.System {
		t.Errorf("system mismatch: %s vs %s", decoded.System, req.System)
	}
}
