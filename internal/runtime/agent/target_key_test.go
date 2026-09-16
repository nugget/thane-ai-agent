package agent

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// toolCallWithArgs is one model turn that calls tool with args.
func toolCallWithArgs(id, tool string, args map[string]any) *llm.ChatResponse {
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
					Arguments: args,
				},
			}},
		},
	}
}

// TestRequestTargetKeyReachesEngine pins the wiring from a request's
// TargetKey to the engine: the response's tool tally carries each call
// under the target the function names, and a request without one
// reports the empty target.
func TestRequestTargetKeyReachesEngine(t *testing.T) {
	const tool = "contact_dossier_write"
	byContact := func(_ string, args map[string]any) string {
		id, _ := args["contact_id"].(string)
		return id
	}
	cases := []struct {
		name       string
		targetKey  func(string, map[string]any) string
		wantTarget string
	}{
		{name: "target key names the contact", targetKey: byContact, wantTarget: "alice"},
		{name: "no target key is the empty target", wantTarget: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockLLM{responses: []*llm.ChatResponse{
				toolCallWithArgs("call-1", tool, map[string]any{"contact_id": "alice"}),
				{Model: "test-model", Message: llm.Message{Role: "assistant", Content: "done"}},
			}}
			loop := buildTestLoop(mock, []string{tool})

			resp, err := loop.Run(context.Background(), &Request{
				Messages:  []Message{{Role: "user", Content: "record what you learned about Alice"}},
				TargetKey: tc.targetKey,
			}, nil)
			if err != nil {
				t.Fatalf("Run() error: %v", err)
			}
			targets := resp.ToolOutcomes[tool].Targets
			if got, ok := targets[tc.wantTarget]; len(targets) != 1 || !ok || got.Calls != 1 {
				t.Errorf("Targets = %+v, want one call under %q", targets, tc.wantTarget)
			}
		})
	}
}
