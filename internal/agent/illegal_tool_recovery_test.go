package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestIllegalToolCall_RecoveryIteration(t *testing.T) {
	// When the model calls an unavailable tool, it should get one
	// recovery iteration with tools still enabled so it can pivot to
	// a legal tool or respond with text.
	legalToolExecuted := false

	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// Iteration 1: model calls an unavailable tool.
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
							Name:      "file_grep",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 10,
			},
			// Iteration 2 (recovery): model pivots to a legal tool.
			{
				Model: "test-model",
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID: "call-2",
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{
							Name:      "web_search",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  200,
				OutputTokens: 10,
			},
			// Iteration 3: model responds with text.
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Here are the results."},
				InputTokens:  300,
				OutputTokens: 15,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"web_search"})

	// Register file_grep in the full registry but exclude it from the
	// request, simulating a capability-filtered tool.
	loop.tools.Register(&tools.Tool{
		Name:        "file_grep",
		Description: "Search files",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			t.Error("file_grep should not have executed")
			return "", nil
		},
	})

	// Override web_search handler to track execution.
	loop.tools.Register(&tools.Tool{
		Name:        "web_search",
		Description: "Search the web",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			legalToolExecuted = true
			return "search results", nil
		},
	})

	resp, err := loop.Run(context.Background(), &Request{
		Messages:     []Message{{Role: "user", Content: "search for something"}},
		ExcludeTools: []string{"file_grep"},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// The legal tool should have executed during the recovery iteration.
	if !legalToolExecuted {
		t.Error("web_search was not executed; recovery iteration should have allowed it")
	}

	// The recovery iteration (call index 1) should have had tools enabled.
	if len(mock.calls) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(mock.calls))
	}
	if mock.calls[1].Tools == nil {
		t.Error("recovery iteration (call 1) had tools=nil; should have tools enabled")
	}

	// The first call's tool result should contain the error message.
	foundError := false
	for _, m := range mock.calls[1].Messages {
		if m.Role == "tool" && strings.Contains(m.Content, "not available") {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Error("expected 'not available' error in tool result messages for recovery call")
	}

	if resp == nil || resp.Content == "" {
		t.Error("expected a non-empty response")
	}
}

func TestIllegalToolCall_RepeatedStrikesBreak(t *testing.T) {
	// If the model calls an unavailable tool twice in a row, the loop
	// should break and force a text-only response.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// Iteration 1: illegal tool call.
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
							Name:      "file_grep",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 10,
			},
			// Iteration 2 (recovery): model calls ANOTHER unavailable tool.
			{
				Model: "test-model",
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID: "call-2",
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{
							Name:      "file_grep",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  200,
				OutputTokens: 10,
			},
			// Post-loop recovery: forced text response (tools=nil).
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "I cannot access file tools."},
				InputTokens:  300,
				OutputTokens: 15,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"web_search"})
	loop.tools.Register(&tools.Tool{
		Name:        "file_grep",
		Description: "Search files",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			t.Error("file_grep should not have executed")
			return "", nil
		},
	})

	resp, err := loop.Run(context.Background(), &Request{
		Messages:     []Message{{Role: "user", Content: "grep for something"}},
		ExcludeTools: []string{"file_grep"},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// After 2 strikes, the final LLM call should have tools=nil.
	lastCall := mock.calls[len(mock.calls)-1]
	if lastCall.Tools != nil {
		t.Error("final recovery call should have tools=nil after repeated illegal calls")
	}

	if resp == nil || resp.Content == "" {
		t.Error("expected a non-empty response")
	}
}
