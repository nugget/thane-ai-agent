package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestRepairToolCall_RepairsInventedCapabilityToolName(t *testing.T) {
	forgeToolCalled := false

	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model: "test-model",
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID: "call-req-cap",
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{
							Name:      "forge_capability",
							Arguments: map[string]any{},
						},
					}},
				},
			},
			{
				Model: "test-model",
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID: "call-forge",
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{
							Name:      "forge_tool",
							Arguments: map[string]any{},
						},
					}},
				},
			},
			{
				Model:   "test-model",
				Message: llm.Message{Role: "assistant", Content: "Done."},
			},
		},
	}

	capTags := map[string]config.CapabilityTagConfig{
		"forge": {
			Description: "Forge tools",
			Tools:       []string{"forge_tool"},
		},
	}

	loop := setupCapabilityLoop(mock, []string{"forge_tool"}, capTags)
	loop.UseCapabilitySurface(tools.BuildCapabilityManifest(
		map[string][]string{"forge": {"forge_tool"}},
		map[string]string{"forge": "Forge tools"},
		nil,
		nil,
	))
	loop.Tools().Register(&tools.Tool{
		Name:        "forge_tool",
		Description: "test forge tool",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			forgeToolCalled = true
			return "forge result", nil
		},
	})

	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "Activate forge"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !forgeToolCalled {
		t.Fatal("forge_tool was not called after repairing forge_capability")
	}
	if resp == nil || resp.Content == "" {
		t.Fatal("expected non-empty response")
	}
	if len(mock.calls) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(mock.calls))
	}
	foundActivationResult := false
	for _, m := range mock.calls[1].Messages {
		if m.Role == "tool" && strings.Contains(m.Content, "Tag **forge** activated.") {
			foundActivationResult = true
			break
		}
	}
	if !foundActivationResult {
		t.Fatalf("recovery call messages = %#v, want repaired activate_tag tool result", mock.calls[1].Messages)
	}
}
