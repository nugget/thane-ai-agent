package agent

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// recordingBindingProvider is a tag context provider that records the
// binding it observed on every invocation.
type recordingBindingProvider struct {
	seen []string
}

func (p *recordingBindingProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketLiveState
}

func (p *recordingBindingProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	p.seen = append(p.seen, looppkg.BindingFromContext(ctx, looppkg.BindingForgeAccount))
	return "bound=" + looppkg.BindingFromContext(ctx, looppkg.BindingForgeAccount), nil
}

// TestRequestBindingsReachContextProvidersEveryIteration runs an actual
// agent request through the transport seam, which no other binding test
// does — they exercise the context helper and the forge service
// directly, so a broken stamp leaves them all green.
//
// It caught a real production defect. The prompt is not built once:
// OnIterationStart rebuilds it on every iteration after the first, from
// the iteration context rather than the local promptCtx that carried
// the stamp. Binding only promptCtx narrowed iteration 0 and handed
// back the full account list from iteration 1 onward, so a bound loop
// was shown a credential its own tool calls would refuse.
func TestRequestBindingsReachContextProvidersEveryIteration(t *testing.T) {
	// Two responses: the first with a tool call so the run continues to
	// a second iteration, which is where the rebuild happens.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:       "test-model",
				Message:     llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{newToolCall("c1", "base_tool")}},
				InputTokens: 100, OutputTokens: 10,
			},
			{
				Model:       "test-model",
				Message:     llm.Message{Role: "assistant", Content: "Done."},
				InputTokens: 100, OutputTokens: 10,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"base_tool"})
	loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"forge": {Description: "Forge", Tools: []string{"base_tool"}, Core: true},
	}, nil)

	provider := &recordingBindingProvider{}
	loop.RegisterTagContextProvider("forge", provider)

	if _, err := loop.Run(context.Background(), &Request{
		Messages:    []Message{{Role: "user", Content: "go"}},
		InitialTags: []string{"forge"},
		Bindings:    map[string]string{looppkg.BindingForgeAccount: "github-readonly"},
	}, nil); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(provider.seen) == 0 {
		t.Fatal("context provider was never invoked; the test cannot observe the seam")
	}
	for i, got := range provider.seen {
		if got != "github-readonly" {
			t.Errorf("provider invocation %d saw binding %q, want %q — the stamp did not reach this prompt build",
				i, got, "github-readonly")
		}
	}
	t.Logf("provider invoked %d times, all bound", len(provider.seen))
}

// newToolCall builds a tool call with the nested Function shape.
func newToolCall(id, name string) llm.ToolCall {
	tc := llm.ToolCall{ID: id}
	tc.Function.Name = name
	tc.Function.Arguments = map[string]any{}
	return tc
}
