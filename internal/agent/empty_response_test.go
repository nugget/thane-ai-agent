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

func TestDeferredText_SkipsNudge(t *testing.T) {
	// Issue #347: when iter 0 returns text + tool call and iter 1 returns
	// empty, the deferred text from iter 0 should be used as the final
	// response — no nudge, no duplicate.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// Iter 0: text + tool call
			{
				Model: "test-model",
				Message: llm.Message{
					Role:    "assistant",
					Content: "Let me check that for you.",
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
			// Iter 1: empty (model thinks it already said its piece)
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: ""},
				InputTokens:  200,
				OutputTokens: 2,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"recall_fact"})

	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "what's the weather"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Should have made only 2 LLM calls — no nudge needed.
	if len(mock.calls) != 2 {
		t.Fatalf("expected 2 LLM calls (no nudge), got %d", len(mock.calls))
	}

	// Response should be the deferred text from iter 0.
	if resp.Content != "Let me check that for you." {
		t.Errorf("response content = %q, want %q", resp.Content, "Let me check that for you.")
	}

	// Verify no nudge message was injected.
	for _, call := range mock.calls {
		for _, msg := range call.Messages {
			if msg.Role == "user" && msg.Content == prompts.EmptyResponseNudge {
				t.Error("nudge message should not be present when deferred text exists")
			}
		}
	}
}

func TestDeferredText_FreshTextOverrides(t *testing.T) {
	// When the model produces new text after tool execution, the deferred
	// text is discarded and the fresh response is used.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// Iter 0: text + tool call
			{
				Model: "test-model",
				Message: llm.Message{
					Role:    "assistant",
					Content: "Checking now...",
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
				OutputTokens: 30,
			},
			// Iter 1: fresh text informed by tool result
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "The lights are all on."},
				InputTokens:  200,
				OutputTokens: 15,
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

	// Should have made 2 LLM calls.
	if len(mock.calls) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(mock.calls))
	}

	// Response should be the fresh text, not the deferred text.
	if resp.Content != "The lights are all on." {
		t.Errorf("response content = %q, want %q", resp.Content, "The lights are all on.")
	}
}

func TestDeferredText_StrippedFromContext(t *testing.T) {
	// The text from a mixed (text + tool_call) response should be stripped
	// from the assistant message in llmMessages so the model doesn't see
	// its own text and restate it.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// Iter 0: text + tool call
			{
				Model: "test-model",
				Message: llm.Message{
					Role:    "assistant",
					Content: "Here's what I found.",
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
				OutputTokens: 40,
			},
			// Iter 1: text response
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Done."},
				InputTokens:  200,
				OutputTokens: 5,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"recall_fact"})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "check something"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Inspect the messages sent to the second LLM call. The assistant
	// message from iter 0 should have empty Content (text stripped).
	secondCall := mock.calls[1]
	for _, msg := range secondCall.Messages {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			if msg.Content != "" {
				t.Errorf("assistant message with tool calls should have empty Content, got %q", msg.Content)
			}
		}
	}
}
