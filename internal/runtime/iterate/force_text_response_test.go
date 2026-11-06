package iterate

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// TestEngine_ForceTextFiresOnLLMResponse pins the force-text recovery
// call as a model call like any other: OnLLMResponse sees it under the
// iteration index it is recorded at, carrying its stop reason and
// tool-call provenance, so the per-call response line counts it.
func TestEngine_ForceTextFiresOnLLMResponse(t *testing.T) {
	t.Parallel()

	forced := func() *llm.ChatResponse {
		resp := textResponse("forced reply")
		resp.StopReason = "stop"
		resp.TextToolAction = llm.TextToolActionStrippedTrailing
		return resp
	}

	tests := []struct {
		name       string
		responses  []*llm.ChatResponse
		configure  func(*Config)
		wantReason string
	}{
		{
			name: "max iterations",
			responses: []*llm.ChatResponse{
				toolCallResponse(makeToolCall("search", map[string]any{"q": "a"})),
				forced(),
			},
			configure:  func(c *Config) { c.MaxIterations = 1 },
			wantReason: ExhaustMaxIterations,
		},
		{
			name: "illegal tool",
			responses: []*llm.ChatResponse{
				toolCallResponse(makeToolCall("banned_tool", nil)),
				toolCallResponse(makeToolCall("banned_tool", nil)),
				forced(),
			},
			configure: func(c *Config) {
				c.CheckToolAvail = func(name string) bool { return name != "banned_tool" }
			},
			wantReason: ExhaustIllegalTool,
		},
		{
			name: "token budget",
			responses: []*llm.ChatResponse{
				toolCallResponse(makeToolCall("search", map[string]any{"q": "a"})),
				toolCallResponse(makeToolCall("search", map[string]any{"q": "b"})),
				forced(),
			},
			configure: func(c *Config) {
				c.CheckBudget = func(total int) bool { return total >= 20 }
			},
			wantReason: ExhaustTokenBudget,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := &mockLLM{responses: tt.responses}
			cfg := baseCfg(mock, &mockExecutor{results: map[string]string{"search": "ok"}})
			type seen struct {
				index int
				resp  llm.ChatResponse
			}
			var got []seen
			cfg.OnLLMResponse = func(_ context.Context, resp *llm.ChatResponse, i int) {
				got = append(got, seen{index: i, resp: *resp})
			}
			tt.configure(&cfg)

			result, err := (&Engine{}).Run(context.Background(), cfg, baseMessages())
			if err != nil {
				t.Fatal(err)
			}
			if result.ExhaustReason != tt.wantReason || result.Content != "forced reply" {
				t.Fatalf("force-text path not exercised: reason=%q content=%q", result.ExhaustReason, result.Content)
			}
			if len(got) != len(tt.responses) {
				t.Fatalf("OnLLMResponse fired %d times for %d model calls", len(got), len(tt.responses))
			}
			last := got[len(got)-1]
			wantIndex := result.Iterations[len(result.Iterations)-1].Index
			if last.index != wantIndex {
				t.Errorf("force-text callback index = %d, want recorded iteration %d", last.index, wantIndex)
			}
			if last.resp.StopReason != "stop" || last.resp.TextToolAction != llm.TextToolActionStrippedTrailing {
				t.Errorf("force-text callback response = %q/%q, want stop/%q",
					last.resp.StopReason, last.resp.TextToolAction, llm.TextToolActionStrippedTrailing)
			}
		})
	}
}
