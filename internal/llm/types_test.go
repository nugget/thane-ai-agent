package llm

import (
	"encoding/json"
	"testing"
	"time"
)

// Representative Ollama /api/chat responses captured from real interactions.
// These are the actual wire-format payloads Thane must handle correctly.

func TestOllamaWireResponse_BasicChat(t *testing.T) {
	// Real Ollama response: simple text reply, no tools
	raw := `{
		"model": "qwen3:4b",
		"created_at": "2026-02-11T15:00:00.123456789Z",
		"message": {
			"role": "assistant",
			"content": "The kitchen light is currently on."
		},
		"done": true,
		"total_duration": 1234567890,
		"load_duration": 100000000,
		"prompt_eval_count": 42,
		"prompt_eval_duration": 500000000,
		"eval_count": 15,
		"eval_duration": 600000000
	}`

	var wire ollamaWireResponse
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	resp := wire.toChatResponse()

	if resp.Model != "qwen3:4b" {
		t.Errorf("Model = %q, want %q", resp.Model, "qwen3:4b")
	}
	if resp.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero, expected parsed time")
	}
	if resp.CreatedAt.Year() != 2026 || resp.CreatedAt.Month() != time.February {
		t.Errorf("CreatedAt = %v, expected 2026-02", resp.CreatedAt)
	}
	if resp.Message.Role != "assistant" {
		t.Errorf("Message.Role = %q, want %q", resp.Message.Role, "assistant")
	}
	if resp.Message.Content != "The kitchen light is currently on." {
		t.Errorf("Message.Content = %q", resp.Message.Content)
	}
	if !resp.Done {
		t.Error("Done = false, want true")
	}
	if resp.InputTokens != 42 {
		t.Errorf("InputTokens = %d, want 42", resp.InputTokens)
	}
	if resp.OutputTokens != 15 {
		t.Errorf("OutputTokens = %d, want 15", resp.OutputTokens)
	}
	if resp.TotalDuration != 1234567890*time.Nanosecond {
		t.Errorf("TotalDuration = %v, want ~1.2s", resp.TotalDuration)
	}
	if resp.LoadDuration != 100*time.Millisecond {
		t.Errorf("LoadDuration = %v, want 100ms", resp.LoadDuration)
	}
	if resp.EvalDuration != 600*time.Millisecond {
		t.Errorf("EvalDuration = %v, want 600ms", resp.EvalDuration)
	}
}

func TestOllamaWireResponse_WithToolCalls(t *testing.T) {
	// Ollama response with native tool_calls
	raw := `{
		"model": "qwen2.5:72b",
		"created_at": "2026-02-11T15:01:00Z",
		"message": {
			"role": "assistant",
			"content": "",
			"tool_calls": [
				{
					"function": {
						"name": "get_state",
						"arguments": {"entity_id": "light.kitchen"}
					}
				}
			]
		},
		"done": true,
		"prompt_eval_count": 128,
		"eval_count": 24
	}`

	var wire ollamaWireResponse
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	resp := wire.toChatResponse()

	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("ToolCalls count = %d, want 1", len(resp.Message.ToolCalls))
	}
	tc := resp.Message.ToolCalls[0]
	if tc.Function.Name != "get_state" {
		t.Errorf("tool name = %q, want %q", tc.Function.Name, "get_state")
	}
	if tc.Function.Arguments["entity_id"] != "light.kitchen" {
		t.Errorf("entity_id = %v", tc.Function.Arguments["entity_id"])
	}
	if resp.InputTokens != 128 {
		t.Errorf("InputTokens = %d, want 128", resp.InputTokens)
	}
}

func TestOllamaWireResponse_StreamChunk(t *testing.T) {
	// Intermediate streaming chunk (done=false, partial content)
	raw := `{
		"model": "qwen3:4b",
		"created_at": "2026-02-11T15:02:00Z",
		"message": {
			"role": "assistant",
			"content": "The"
		},
		"done": false
	}`

	var wire ollamaWireResponse
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	resp := wire.toChatResponse()

	if resp.Done {
		t.Error("Done = true, want false for stream chunk")
	}
	if resp.Message.Content != "The" {
		t.Errorf("Content = %q, want %q", resp.Message.Content, "The")
	}
	// Token counts should be zero for intermediate chunks
	if resp.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0 for stream chunk", resp.InputTokens)
	}
}

func TestOllamaWireResponse_MissingTimestamp(t *testing.T) {
	// Some Ollama responses may have empty or missing created_at
	raw := `{
		"model": "qwen3:4b",
		"created_at": "",
		"message": {"role": "assistant", "content": "hello"},
		"done": true
	}`

	var wire ollamaWireResponse
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	resp := wire.toChatResponse()

	// Should not crash, CreatedAt should be zero time
	if !resp.CreatedAt.IsZero() {
		t.Errorf("expected zero time for empty created_at, got %v", resp.CreatedAt)
	}
	// Everything else should still work
	if resp.Message.Content != "hello" {
		t.Errorf("Content = %q", resp.Message.Content)
	}
}

func TestOllamaWireResponse_ZeroDurations(t *testing.T) {
	// Response with no timing info (some error paths)
	raw := `{
		"model": "qwen3:4b",
		"created_at": "2026-02-11T15:00:00Z",
		"message": {"role": "assistant", "content": "ok"},
		"done": true
	}`

	var wire ollamaWireResponse
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	resp := wire.toChatResponse()

	if resp.TotalDuration != 0 {
		t.Errorf("TotalDuration = %v, want 0", resp.TotalDuration)
	}
	if resp.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0", resp.InputTokens)
	}
}

func TestOllamaWireResponse_MultipleToolCalls(t *testing.T) {
	// Model returns multiple tool calls in one response
	raw := `{
		"model": "qwen2.5:72b",
		"created_at": "2026-02-11T15:03:00Z",
		"message": {
			"role": "assistant",
			"content": "",
			"tool_calls": [
				{
					"function": {
						"name": "get_state",
						"arguments": {"entity_id": "light.kitchen"}
					}
				},
				{
					"function": {
						"name": "get_state",
						"arguments": {"entity_id": "light.bedroom"}
					}
				}
			]
		},
		"done": true,
		"eval_count": 50
	}`

	var wire ollamaWireResponse
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	resp := wire.toChatResponse()

	if len(resp.Message.ToolCalls) != 2 {
		t.Fatalf("ToolCalls = %d, want 2", len(resp.Message.ToolCalls))
	}
	if resp.Message.ToolCalls[0].Function.Arguments["entity_id"] != "light.kitchen" {
		t.Error("first tool call entity mismatch")
	}
	if resp.Message.ToolCalls[1].Function.Arguments["entity_id"] != "light.bedroom" {
		t.Error("second tool call entity mismatch")
	}
}

func TestOllamaWireResponse_LargeTokenCounts(t *testing.T) {
	// Verify no truncation/overflow for realistic large counts
	raw := `{
		"model": "qwen2.5:72b",
		"created_at": "2026-02-11T15:00:00Z",
		"message": {"role": "assistant", "content": "analysis complete"},
		"done": true,
		"prompt_eval_count": 32768,
		"eval_count": 4096,
		"total_duration": 45000000000,
		"eval_duration": 30000000000
	}`

	var wire ollamaWireResponse
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	resp := wire.toChatResponse()

	if resp.InputTokens != 32768 {
		t.Errorf("InputTokens = %d, want 32768", resp.InputTokens)
	}
	if resp.OutputTokens != 4096 {
		t.Errorf("OutputTokens = %d, want 4096", resp.OutputTokens)
	}
	if resp.TotalDuration != 45*time.Second {
		t.Errorf("TotalDuration = %v, want 45s", resp.TotalDuration)
	}
	if resp.EvalDuration != 30*time.Second {
		t.Errorf("EvalDuration = %v, want 30s", resp.EvalDuration)
	}
}

// Anthropic response conversion tests

func TestConvertFromAnthropic_TextOnly(t *testing.T) {
	resp := &anthropicResponse{
		Model: "claude-opus-4-20250514",
		Role:  "assistant",
		Content: []anthropicContent{
			{Type: "text", Text: "The lights are off."},
		},
		StopReason: "end_turn",
		Usage:      anthropicUsage{InputTokens: 100, OutputTokens: 25},
	}

	result := convertFromAnthropic(resp)

	if result.Model != "claude-opus-4-20250514" {
		t.Errorf("Model = %q", result.Model)
	}
	if result.Message.Content != "The lights are off." {
		t.Errorf("Content = %q", result.Message.Content)
	}
	if result.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", result.InputTokens)
	}
	if result.OutputTokens != 25 {
		t.Errorf("OutputTokens = %d, want 25", result.OutputTokens)
	}
	if !result.Done {
		t.Error("Done = false, want true")
	}
	if len(result.Message.ToolCalls) != 0 {
		t.Errorf("ToolCalls = %d, want 0", len(result.Message.ToolCalls))
	}
}

func TestConvertFromAnthropic_ToolUse(t *testing.T) {
	resp := &anthropicResponse{
		Model: "claude-opus-4-20250514",
		Role:  "assistant",
		Content: []anthropicContent{
			{Type: "text", Text: "Let me check that."},
			{
				Type:  "tool_use",
				ID:    "toolu_01ABC",
				Name:  "control_device",
				Input: map[string]any{"entity": "light.office", "action": "turn_on"},
			},
		},
		StopReason: "tool_use",
		Usage:      anthropicUsage{InputTokens: 200, OutputTokens: 50},
	}

	result := convertFromAnthropic(resp)

	if result.Message.Content != "Let me check that." {
		t.Errorf("Content = %q", result.Message.Content)
	}
	if len(result.Message.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %d, want 1", len(result.Message.ToolCalls))
	}

	tc := result.Message.ToolCalls[0]
	if tc.ID != "toolu_01ABC" {
		t.Errorf("ToolCall.ID = %q, want %q", tc.ID, "toolu_01ABC")
	}
	if tc.Function.Name != "control_device" {
		t.Errorf("ToolCall.Function.Name = %q", tc.Function.Name)
	}
	if tc.Function.Arguments["entity"] != "light.office" {
		t.Errorf("entity arg = %v", tc.Function.Arguments["entity"])
	}
	if tc.Function.Arguments["action"] != "turn_on" {
		t.Errorf("action arg = %v", tc.Function.Arguments["action"])
	}
}

func TestConvertFromAnthropic_MultipleToolCalls(t *testing.T) {
	resp := &anthropicResponse{
		Model: "claude-opus-4-20250514",
		Role:  "assistant",
		Content: []anthropicContent{
			{
				Type:  "tool_use",
				ID:    "toolu_01",
				Name:  "get_state",
				Input: map[string]any{"entity_id": "light.kitchen"},
			},
			{
				Type:  "tool_use",
				ID:    "toolu_02",
				Name:  "get_state",
				Input: map[string]any{"entity_id": "light.bedroom"},
			},
		},
		StopReason: "tool_use",
	}

	result := convertFromAnthropic(resp)

	if len(result.Message.ToolCalls) != 2 {
		t.Fatalf("ToolCalls = %d, want 2", len(result.Message.ToolCalls))
	}
	if result.Message.ToolCalls[0].ID != "toolu_01" {
		t.Errorf("first tool ID = %q", result.Message.ToolCalls[0].ID)
	}
	if result.Message.ToolCalls[1].ID != "toolu_02" {
		t.Errorf("second tool ID = %q", result.Message.ToolCalls[1].ID)
	}
}

func TestConvertFromAnthropic_EmptyContent(t *testing.T) {
	resp := &anthropicResponse{
		Model:      "claude-opus-4-20250514",
		Role:       "assistant",
		Content:    []anthropicContent{},
		StopReason: "end_turn",
		Usage:      anthropicUsage{InputTokens: 50, OutputTokens: 0},
	}

	result := convertFromAnthropic(resp)

	if result.Message.Content != "" {
		t.Errorf("Content = %q, want empty", result.Message.Content)
	}
	if len(result.Message.ToolCalls) != 0 {
		t.Errorf("ToolCalls = %d, want 0", len(result.Message.ToolCalls))
	}
}

// ChatResponse field type safety tests

func TestChatResponse_TimeTypeSafety(t *testing.T) {
	// Verify we can do time operations on ChatResponse fields
	// (This would fail at compile time if CreatedAt were string)
	resp := ChatResponse{
		CreatedAt:     time.Now(),
		TotalDuration: 5 * time.Second,
		EvalDuration:  3 * time.Second,
	}

	// These operations prove the types are correct
	_ = resp.CreatedAt.Unix()
	_ = resp.TotalDuration.Seconds()
	_ = resp.EvalDuration.Milliseconds()

	if resp.TotalDuration.Seconds() != 5.0 {
		t.Errorf("TotalDuration.Seconds() = %f, want 5.0", resp.TotalDuration.Seconds())
	}

	// Duration arithmetic works
	overhead := resp.TotalDuration - resp.EvalDuration
	if overhead != 2*time.Second {
		t.Errorf("overhead = %v, want 2s", overhead)
	}
}

func TestChatResponse_ZeroValuesSafe(t *testing.T) {
	// Zero-value ChatResponse should be safe to use
	var resp ChatResponse

	if !resp.CreatedAt.IsZero() {
		t.Error("zero ChatResponse.CreatedAt should be zero time")
	}
	if resp.InputTokens != 0 {
		t.Error("zero ChatResponse.InputTokens should be 0")
	}
	if resp.TotalDuration != 0 {
		t.Error("zero ChatResponse.TotalDuration should be 0")
	}
	if resp.Done {
		t.Error("zero ChatResponse.Done should be false")
	}
}
