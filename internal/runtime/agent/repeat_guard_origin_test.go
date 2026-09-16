package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// repeatedToolCallResponse is one model turn that calls tool with no
// arguments, so every such turn is an identical call to the repeat guard.
func repeatedToolCallResponse(id, tool string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Model: "test-model",
		Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: id,
				Function: struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				}{
					Name:      tool,
					Arguments: map[string]any{},
				},
			}},
		},
	}
}

// TestRepeatGuardWordingFollowsMessageOrigin pins the wiring from a
// request's MessageOrigin to the refusal the repeat guard returns. Only
// a turn someone is waiting on — a channel message or an API request —
// may be told to stop calling tools and answer; a loop wake or an
// unstamped internal run must get the neutral text, or a loop in the
// middle of a durable write reads the refusal as "abandon the write".
func TestRepeatGuardWordingFollowsMessageOrigin(t *testing.T) {
	const (
		tool      = "web_search"
		refusedID = "call-4"
	)
	cases := []struct {
		name   string
		origin string
		want   string
	}{
		{name: "channel turn", origin: memory.OriginChannel, want: prompts.RepeatedToolCallInteractive(tool, 4)},
		{name: "api turn", origin: memory.OriginAPI, want: prompts.RepeatedToolCallInteractive(tool, 4)},
		{name: "loop wake", origin: memory.OriginWake, want: prompts.RepeatedToolCall(tool, 4)},
		{name: "unstamped internal run", origin: "", want: prompts.RepeatedToolCall(tool, 4)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			responses := make([]*llm.ChatResponse, 0, 5)
			for i := 1; i <= 4; i++ {
				responses = append(responses, repeatedToolCallResponse(fmt.Sprintf("call-%d", i), tool))
			}
			responses = append(responses, &llm.ChatResponse{
				Model:   "test-model",
				Message: llm.Message{Role: "assistant", Content: "done"},
			})
			mock := &mockLLM{responses: responses}
			loop := buildTestLoop(mock, []string{tool})

			if _, err := loop.Run(context.Background(), &Request{
				Messages:      []Message{{Role: "user", Content: "search for something"}},
				MessageOrigin: tc.origin,
			}, nil); err != nil {
				t.Fatalf("Run() error: %v", err)
			}

			var got string
			var found bool
			for _, call := range mock.calls {
				for _, m := range call.Messages {
					if m.Role == "tool" && m.ToolCallID == refusedID {
						got, found = m.Content, true
					}
				}
			}
			if !found {
				t.Fatalf("no tool result for %s reached the model", refusedID)
			}
			if got != tc.want {
				t.Errorf("refused call result = %q, want %q", got, tc.want)
			}
		})
	}
}
