package delegate

import (
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

func TestExtractToolsCalled(t *testing.T) {
	tests := []struct {
		name     string
		messages []llm.Message
		want     map[string]int
	}{
		{
			name:     "empty messages",
			messages: nil,
			want:     nil,
		},
		{
			name: "no tool calls",
			messages: []llm.Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi"},
			},
			want: nil,
		},
		{
			name: "single tool call",
			messages: []llm.Message{
				{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{Name: "get_state"},
					}},
				},
			},
			want: map[string]int{"get_state": 1},
		},
		{
			name: "repeated tool calls",
			messages: []llm.Message{
				{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{Name: "get_state"}},
						{Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{Name: "list_entities"}},
					},
				},
				{Role: "tool", Content: "result1"},
				{Role: "tool", Content: "result2"},
				{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{Name: "get_state"}},
					},
				},
			},
			want: map[string]int{"get_state": 2, "list_entities": 1},
		},
		{
			name: "mixed messages with text-only assistant",
			messages: []llm.Message{
				{Role: "system", Content: "system prompt"},
				{Role: "user", Content: "do something"},
				{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{Name: "call_service"}},
					},
				},
				{Role: "tool", Content: "ok"},
				{Role: "assistant", Content: "Done."},
			},
			want: map[string]int{"call_service": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractToolsCalled(tt.messages)
			if tt.want == nil {
				if got != nil {
					t.Errorf("ExtractToolsCalled() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ExtractToolsCalled() count = %d, want %d; got %v", len(got), len(tt.want), got)
			}
			for name, want := range tt.want {
				if got[name] != want {
					t.Errorf("ExtractToolsCalled()[%q] = %d, want %d", name, got[name], want)
				}
			}
		})
	}
}
