package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

type adapterUsageClient struct {
	calls  int
	cancel context.CancelFunc
	fail   error
}

func (c *adapterUsageClient) Chat(ctx context.Context, model string, messages []llm.Message, defs []map[string]any) (*llm.ChatResponse, error) {
	return c.ChatStream(ctx, model, messages, defs, nil)
}

func (c *adapterUsageClient) ChatStream(_ context.Context, model string, _ []llm.Message, _ []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	c.calls++
	response := &llm.ChatResponse{Model: model, InputTokens: c.calls * 100, OutputTokens: c.calls * 10, UpstreamRequestID: fmt.Sprintf("provider-call-%d", c.calls)}
	if c.calls == 1 {
		call := llm.ToolCall{ID: "probe-call"}
		call.Function.Name = "accounting_probe"
		call.Function.Arguments = map[string]any{}
		response.Message = llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}
		return response, nil
	}
	if c.cancel != nil {
		c.cancel()
	}
	return response, c.fail
}

func (c *adapterUsageClient) Ping(context.Context) error { return nil }

func TestLoopAdapterPreservesUsageObserverOnAgentFailure(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			ctx, cancel := context.WithCancel(logging.WithLogger(context.Background(), logger))
			defer cancel()
			provider := &adapterUsageClient{fail: errors.New("interrupted provider stream")}
			if canceled {
				provider.fail = context.Canceled
				provider.cancel = cancel
			}
			multi := llm.NewMultiClient(nil)
			multi.AddProvider("cloud", provider)
			multi.AddRoute("cloud/claude-primary", "cloud", "claude-primary")
			agentLoop, err := agent.NewLoop(agent.LoopOptions{
				Logger: logger, Memory: memory.NewStore(100), LLM: multi, Model: "cloud/claude-primary",
			})
			if err != nil {
				t.Fatal(err)
			}
			agentLoop.SetUsageRecorder(nil, map[string]config.PricingEntry{
				"cloud/claude-primary": {InputPerMillion: 2, OutputPerMillion: 10},
			}, nil)
			agentLoop.Tools().Register(&tools.Tool{
				Name: "accounting_probe", Description: "Inspect the source.",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
				Handler:    func(context.Context, map[string]any) (string, error) { return "source evidence", nil },
			})
			var observed []usage.Record
			ctx = usage.WithObserver(ctx, func(record usage.Record) { observed = append(observed, record) })
			adapter := &loopAdapter{agentLoop: agentLoop}
			response, err := adapter.Run(ctx, looppkg.Request{
				Model: "cloud/claude-primary", ConversationID: "adapter-conversation",
				UsageRole: "interactive", UsageTaskName: "api-observer-test",
				AllowedTools: []string{"accounting_probe"},
				Messages:     []looppkg.Message{{Role: "user", Content: "Inspect the source and explain it."}},
			}, nil)
			if response != nil || !errors.Is(err, provider.fail) || provider.calls != 2 {
				t.Fatalf("adapter error contract changed: response=%+v error=%v calls=%d", response, err, provider.calls)
			}
			if len(observed) != 2 {
				t.Fatalf("adapter lost synchronous usage callbacks: %+v", observed)
			}
			for i, record := range observed {
				if record.Model != "cloud/claude-primary" || record.Provider != "anthropic" || record.Resource != "cloud" || record.UpstreamModel != "claude-primary" || record.InputTokens != (i+1)*100 || record.OutputTokens != (i+1)*10 || math.Abs(record.CostUSD-float64(i+1)*0.0003) > 1e-12 {
					t.Errorf("adapter usage call %d = %+v", i, record)
				}
				if record.RequestID == "" || record.RequestID != observed[0].RequestID || record.ConversationID != "adapter-conversation" || record.UpstreamRequestID != fmt.Sprintf("provider-call-%d", i+1) || record.Role != "interactive" || record.TaskName != "api-observer-test" {
					t.Errorf("adapter lost usage provenance: %+v", record)
				}
			}
		})
	}
}
