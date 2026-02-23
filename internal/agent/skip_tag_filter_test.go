package agent

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestSkipTagFilter_Bypasses(t *testing.T) {
	// When SkipTagFilter is true, untagged tools should still be visible
	// and executable even when capability tags are active.
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
							Name:      "untagged_tool",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 10,
			},
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Tool worked."},
				InputTokens:  200,
				OutputTokens: 5,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"tagged_tool"})

	// Register an untagged tool.
	untaggedExecuted := false
	loop.tools.Register(&tools.Tool{
		Name:        "untagged_tool",
		Description: "An untagged tool",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			untaggedExecuted = true
			return "ok", nil
		},
	})

	// Set up capability tags: only "tagged_tool" is tagged.
	loop.SetCapabilityTags(
		map[string]config.CapabilityTagConfig{
			"testing": {
				Tools:        []string{"tagged_tool"},
				AlwaysActive: true,
			},
		},
		nil,
	)

	resp, err := loop.Run(context.Background(), &Request{
		Messages:      []Message{{Role: "user", Content: "use the untagged tool"}},
		SkipTagFilter: true,
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// The untagged tool should have executed because SkipTagFilter=true.
	if !untaggedExecuted {
		t.Error("untagged_tool should have executed with SkipTagFilter=true")
	}

	// Both tools should appear in definitions sent to the model.
	if len(mock.calls) < 1 {
		t.Fatal("expected at least 1 LLM call")
	}
	names := toolNames(mock.calls[0].Tools)
	if !hasName(names, "untagged_tool") {
		t.Errorf("untagged_tool should appear in tool definitions with SkipTagFilter=true: %v", names)
	}
	if !hasName(names, "tagged_tool") {
		t.Errorf("tagged_tool should appear in tool definitions: %v", names)
	}

	if resp == nil || resp.Content == "" {
		t.Error("expected a non-empty response")
	}
}

func TestSkipTagFilter_DefaultPreserves(t *testing.T) {
	// When SkipTagFilter is false (default), untagged tools should be
	// filtered out when capability tags are active.
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
							Name:      "untagged_tool",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 10,
			},
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Tool blocked."},
				InputTokens:  200,
				OutputTokens: 5,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"tagged_tool"})

	// Register an untagged tool.
	untaggedExecuted := false
	loop.tools.Register(&tools.Tool{
		Name:        "untagged_tool",
		Description: "An untagged tool",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			untaggedExecuted = true
			return "ok", nil
		},
	})

	// Set up capability tags: only "tagged_tool" is tagged.
	loop.SetCapabilityTags(
		map[string]config.CapabilityTagConfig{
			"testing": {
				Tools:        []string{"tagged_tool"},
				AlwaysActive: true,
			},
		},
		nil,
	)

	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "use the untagged tool"}},
		// SkipTagFilter defaults to false.
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// The untagged tool should NOT have executed.
	if untaggedExecuted {
		t.Error("untagged_tool should NOT have executed with default SkipTagFilter=false")
	}

	// The untagged tool should NOT appear in definitions sent to the model.
	if len(mock.calls) < 1 {
		t.Fatal("expected at least 1 LLM call")
	}
	names := toolNames(mock.calls[0].Tools)
	if hasName(names, "untagged_tool") {
		t.Errorf("untagged_tool should NOT appear in tool definitions with SkipTagFilter=false: %v", names)
	}
	if !hasName(names, "tagged_tool") {
		t.Errorf("tagged_tool should appear in tool definitions: %v", names)
	}

	if resp == nil || resp.Content == "" {
		t.Error("expected a non-empty response")
	}
}
