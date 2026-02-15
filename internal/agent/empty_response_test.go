package agent

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/prompts"
)

func TestEmptyResponse_NudgeRecovery(t *testing.T) {
	// Simulates the scenario from issue #167: model spends iterations
	// on tool calls, then returns empty content with no tool calls.
	// The loop should inject a nudge and retry once.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// Iter 0: tool call (e.g. session_working_memory read)
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
							Name:      "recall_fact",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 50,
			},
			// Iter 1: empty content, no tool calls (the bug)
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: ""},
				InputTokens:  200,
				OutputTokens: 2,
			},
			// Iter 2 (after nudge): real response
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Hello! How can I help?"},
				InputTokens:  250,
				OutputTokens: 10,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"recall_fact"})

	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "check the lights"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Should have made 3 LLM calls: tool call, empty, nudge recovery.
	if len(mock.calls) != 3 {
		t.Fatalf("expected 3 LLM calls, got %d", len(mock.calls))
	}

	// The nudge message should appear in the third call's messages.
	lastCall := mock.calls[2]
	nudgeFound := false
	for _, msg := range lastCall.Messages {
		if msg.Role == "user" && msg.Content == prompts.EmptyResponseNudge {
			nudgeFound = true
			break
		}
	}
	if !nudgeFound {
		t.Error("nudge message not found in third LLM call")
	}

	// Response should contain the real content from the third call.
	if resp.Content != "Hello! How can I help?" {
		t.Errorf("response content = %q, want %q", resp.Content, "Hello! How can I help?")
	}
}

func TestEmptyResponse_FallbackAfterNudge(t *testing.T) {
	// If the model returns empty content even after the nudge,
	// the loop should return a fallback message.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// Iter 0: tool call
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
							Name:      "recall_fact",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 50,
			},
			// Iter 1: empty content, no tool calls
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: ""},
				InputTokens:  200,
				OutputTokens: 2,
			},
			// Iter 2 (after nudge): still empty
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: ""},
				InputTokens:  250,
				OutputTokens: 2,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"recall_fact"})

	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "check the lights"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Should have made 3 LLM calls.
	if len(mock.calls) != 3 {
		t.Fatalf("expected 3 LLM calls, got %d", len(mock.calls))
	}

	// Response should be the fallback message.
	if resp.Content != prompts.EmptyResponseFallback {
		t.Errorf("response content = %q, want %q", resp.Content, prompts.EmptyResponseFallback)
	}
}

func TestEmptyResponse_FirstIterNotNudged(t *testing.T) {
	// If the model returns empty content on the very first iteration
	// (no prior tool calls), the nudge should NOT trigger because
	// i == 0 — the guard only activates after tool call iterations.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// Iter 0: empty content, no tool calls
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: ""},
				InputTokens:  100,
				OutputTokens: 2,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"recall_fact"})

	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "check the lights"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Only 1 LLM call — no nudge retry.
	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(mock.calls))
	}

	// Response should be empty (no fallback for first-iteration empty).
	if resp.Content != "" {
		t.Errorf("response content = %q, want empty", resp.Content)
	}
}
