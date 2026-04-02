package agent

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/llm"
)

func TestAllowedTools_RestrictsVisibleTools(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Done."},
				InputTokens:  42,
				OutputTokens: 7,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"alpha_tool", "beta_tool"})
	resp, err := loop.Run(context.Background(), &Request{
		Messages:     []Message{{Role: "user", Content: "use the allowed tool"}},
		AllowedTools: []string{"alpha_tool"},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if resp.Content != "Done." {
		t.Fatalf("Content = %q, want %q", resp.Content, "Done.")
	}
	if len(mock.calls) != 1 {
		t.Fatalf("mock call count = %d, want 1", len(mock.calls))
	}

	names := toolNames(mock.calls[0].Tools)
	if !hasName(names, "alpha_tool") {
		t.Fatalf("tools = %v, want alpha_tool present", names)
	}
	if hasName(names, "beta_tool") {
		t.Fatalf("tools = %v, want beta_tool filtered out", names)
	}
}

func TestRun_ResponseIncludesIterationMetadata(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model: "test-model",
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID: "call-1",
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{
							Name:      "alpha_tool",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 10,
			},
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Finished."},
				InputTokens:  120,
				OutputTokens: 15,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"alpha_tool"})
	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "finish the task"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if resp.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", resp.Iterations)
	}
	if resp.Exhausted {
		t.Fatal("Exhausted = true, want false")
	}
	if resp.Model != "test-model" {
		t.Fatalf("Model = %q, want %q", resp.Model, "test-model")
	}
}
