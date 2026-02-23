package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestExcludeTools_BlocksExecution(t *testing.T) {
	// When a tool is in ExcludeTools, the model should not see it in
	// definitions AND calls to it should be rejected with a clear error
	// rather than executing against the full registry.
	toolExecuted := false
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// First iteration: model hallucinates a call to the excluded tool.
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
							Name:      "file_read",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 10,
			},
			// Second iteration: model responds with text.
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "OK, that tool is not available."},
				InputTokens:  200,
				OutputTokens: 5,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"web_search"})

	// Register file_read with a handler that records execution.
	loop.tools.Register(&tools.Tool{
		Name:        "file_read",
		Description: "Read a file",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			toolExecuted = true
			return "file contents", nil
		},
	})

	resp, err := loop.Run(context.Background(), &Request{
		Messages:     []Message{{Role: "user", Content: "read the file"}},
		ExcludeTools: []string{"file_read"},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// The tool should NOT have executed.
	if toolExecuted {
		t.Error("excluded tool 'file_read' was executed; should have been blocked")
	}

	// The model should not have seen file_read in tool definitions.
	if len(mock.calls) < 1 {
		t.Fatal("expected at least 1 LLM call")
	}
	names := toolNames(mock.calls[0].Tools)
	if hasName(names, "file_read") {
		t.Errorf("file_read should not appear in tool definitions: %v", names)
	}
	if !hasName(names, "web_search") {
		t.Errorf("web_search should appear in tool definitions: %v", names)
	}

	// The tool result fed back to the model should contain the error.
	if len(mock.calls) >= 2 {
		msgs := mock.calls[1].Messages
		foundError := false
		for _, m := range msgs {
			if m.Role == "tool" && strings.Contains(m.Content, "not available") {
				foundError = true
				break
			}
		}
		if !foundError {
			t.Error("expected tool result message containing 'not available' error")
		}
	}

	// Verify the run completed successfully.
	if resp == nil || resp.Content == "" {
		t.Error("expected a non-empty response")
	}
}

func TestExcludeTools_AllowedToolStillExecutes(t *testing.T) {
	// Tools NOT in ExcludeTools should execute normally.
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
							Name:      "web_search",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 10,
			},
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Found results."},
				InputTokens:  200,
				OutputTokens: 5,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"file_read", "web_search"})

	resp, err := loop.Run(context.Background(), &Request{
		Messages:     []Message{{Role: "user", Content: "search for something"}},
		ExcludeTools: []string{"file_read"},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if resp == nil || resp.Content == "" {
		t.Error("expected a non-empty response")
	}

	// web_search should have executed (no error in tool result).
	if len(mock.calls) >= 2 {
		msgs := mock.calls[1].Messages
		for _, m := range msgs {
			if m.Role == "tool" && strings.Contains(m.Content, "not available") {
				t.Error("web_search should not have been blocked")
			}
		}
	}
}
