package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// Unlike mockTimeoutLLM, this client can return provider usage together with
// an error, as a stream interrupted after its usage event can do.
type accountingTestClient struct {
	call func(context.Context, string, []map[string]any) (*llm.ChatResponse, error)
}

func (c accountingTestClient) Chat(ctx context.Context, model string, messages []llm.Message, defs []map[string]any) (*llm.ChatResponse, error) {
	return c.ChatStream(ctx, model, messages, defs, nil)
}

func (c accountingTestClient) ChatStream(ctx context.Context, model string, _ []llm.Message, defs []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	return c.call(ctx, model, defs)
}

func (c accountingTestClient) Ping(context.Context) error { return nil }

func newAccountingTestLoop(t *testing.T) (*Loop, *memory.SQLiteStore, *memory.ArchiveStore) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := memory.NewSQLiteStore(filepath.Join(t.TempDir(), "thane.db"), 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	archive, err := memory.NewArchiveStoreFromDB(store.DB(), nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	usageStore, err := usage.NewStore(store.DB(), logger)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.Tool{
		Name: "inspect", Description: "Inspect the source.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		Handler:    func(context.Context, map[string]any) (string, error) { return "source evidence", nil },
	})
	loop := &Loop{
		logger: logger, memory: store, tools: reg, model: "claude-primary",
		archiver:   memory.NewArchiveAdapter(archive, store, logger),
		usageStore: usageStore, retryBaseDelay: time.Millisecond,
		pricing: map[string]config.PricingEntry{
			"claude-primary":  {InputPerMillion: 2, OutputPerMillion: 10},
			"claude-recovery": {InputPerMillion: 8, OutputPerMillion: 40},
		},
	}
	return loop, store, archive
}

func accountingToolResponse() *llm.ChatResponse {
	call := llm.ToolCall{ID: "source-call"}
	call.Function.Name = "inspect"
	call.Function.Arguments = map[string]any{}
	return &llm.ChatResponse{
		Model: "claude-primary", UpstreamRequestID: "upstream-source", InputTokens: 100, OutputTokens: 10,
		Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}},
	}
}

func TestRun_AccountsEachModelCallIncludingRetryUsage(t *testing.T) {
	loop, store, archive := newAccountingTestLoop(t)
	loop.recoveryModel = "claude-recovery"
	var calls []string
	loop.llm = accountingTestClient{call: func(ctx context.Context, model string, defs []map[string]any) (*llm.ChatResponse, error) {
		calls = append(calls, model)
		switch len(calls) {
		case 1:
			return accountingToolResponse(), nil
		case 2:
			return &llm.ChatResponse{Model: model, UpstreamRequestID: "upstream-interrupted", InputTokens: 200, OutputTokens: 20}, errors.New("stream timeout")
		case 3, 4:
			return nil, errors.New("request timeout")
		case 5:
			if model != "claude-recovery" || len(defs) != 0 || ctx.Err() != nil {
				t.Fatalf("invalid recovery call: model=%s tools=%d error=%v", model, len(defs), ctx.Err())
			}
			return &llm.ChatResponse{
				Model: model, UpstreamRequestID: "upstream-recovery", InputTokens: 300, OutputTokens: 30,
				CacheCreationInputTokens: 50, CacheCreation5mInputTokens: 20, CacheCreation1hInputTokens: 30, CacheReadInputTokens: 40,
				Message: llm.Message{Role: "assistant", Content: "Recovered source evidence."},
			}, nil
		default:
			return nil, fmt.Errorf("unexpected model call %d", len(calls))
		}
	}}
	var live logging.RequestContent
	loop.liveRequestRecorder = func(_ context.Context, content logging.RequestContent) { live = content }
	var observed []usage.Record
	ctx := usage.WithObserver(context.Background(), func(record usage.Record) { observed = append(observed, record) })
	response, err := loop.Run(ctx, &Request{
		ConversationID: "mixed-model-accounting", Model: "claude-primary", UsageRole: "delegate", UsageTaskName: "inspect-source",
		Messages: []Message{{Role: "user", Content: "Inspect the source and explain what you find."}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 5 || response.Model != "claude-recovery" || response.InputTokens != 600 || response.OutputTokens != 60 || response.PeakInputTokens != 300 || response.Iterations != 2 {
		t.Fatalf("call/response totals = calls:%v response:%+v", calls, response)
	}
	if response.CacheCreationInputTokens != 50 || response.CacheReadInputTokens != 40 {
		t.Fatalf("cache totals = %+v", response)
	}
	if live.InputTokens != response.InputTokens || live.OutputTokens != response.OutputTokens {
		t.Fatalf("trace totals = %d/%d, response = %d/%d", live.InputTokens, live.OutputTokens, response.InputTokens, response.OutputTokens)
	}
	rows, err := store.DB().Query(`SELECT model, upstream_request_id, input_tokens, output_tokens, cost_usd, session_id, request_id, role, task_name FROM usage_records ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	wantModels := []string{"claude-primary", "claude-primary", "claude-recovery"}
	wantUpstream := []string{"upstream-source", "upstream-interrupted", "upstream-recovery"}
	// Primary rates: $2/$10 per million. Recovery: $8/$40, plus
	// 20 cache writes at 1.25x, 30 at 2x, and 40 reads at 0.1x.
	wantCost := []float64{0.0003, 0.0006, 0.004312}
	if len(observed) != 3 || observed[2].CacheCreation5mInputTokens != 20 || observed[2].CacheCreation1hInputTokens != 30 {
		t.Fatalf("observer lost retry/recovery or cache duration usage: %+v", observed)
	}
	i := 0
	for rows.Next() {
		var model, upstream, session, request, role, task string
		var input, output int
		var cost float64
		if err := rows.Scan(&model, &upstream, &input, &output, &cost, &session, &request, &role, &task); err != nil {
			t.Fatal(err)
		}
		if i >= len(wantModels) {
			t.Fatal("extra usage row for a retry without reported usage")
		}
		if model != wantModels[i] || upstream != wantUpstream[i] || input != (i+1)*100 || output != (i+1)*10 || math.Abs(cost-wantCost[i]) > 1e-12 {
			t.Errorf("usage row %d = model:%s upstream:%s tokens:%d/%d cost:%g", i, model, upstream, input, output, cost)
		}
		if session != loop.archiver.ActiveSessionID("mixed-model-accounting") || request != live.RequestID || role != "delegate" || task != "inspect-source" {
			t.Errorf("usage provenance lost: session=%q request=%q role=%q task=%q", session, request, role, task)
		}
		if observed[i].CostUSD != cost || observed[i].Model != model || observed[i].RequestID != request || observed[i].UpstreamRequestID != upstream {
			t.Errorf("observer and durable record diverged: observed=%+v", observed[i])
		}
		i++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if i != 3 {
		t.Fatalf("usage rows = %d, want three provider usage responses", i)
	}
	iterations, err := archive.GetSessionIterations(loop.archiver.ActiveSessionID("mixed-model-accounting"))
	if err != nil || len(iterations) != 2 || iterations[0].Model != "claude-primary" || iterations[1].Model != "claude-recovery" {
		t.Fatalf("completed iterations = %+v, error = %v", iterations, err)
	}
}

func TestRun_UsageObserverWithoutPersistence(t *testing.T) {
	for _, lightweight := range []bool{false, true} {
		t.Run(fmt.Sprintf("lightweight=%t", lightweight), func(t *testing.T) {
			loop, _, _ := newAccountingTestLoop(t)
			loop.usageStore = nil
			loop.pricing = nil
			loop.ConfigureSessionStores(SessionStoreWiring{Pricing: map[string]config.PricingEntry{
				"claude-primary": {InputPerMillion: 2, OutputPerMillion: 10},
			}})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var observed []usage.Record
			ctx = usage.WithObserver(ctx, func(record usage.Record) { observed = append(observed, record) })
			loop.llm = accountingTestClient{call: func(context.Context, string, []map[string]any) (*llm.ChatResponse, error) {
				cancel()
				return &llm.ChatResponse{Model: "claude-primary", InputTokens: 100, OutputTokens: 10}, context.Canceled
			}}
			response, err := loop.Run(ctx, &Request{
				SkipContext: lightweight,
				Messages:    []Message{{Role: "user", Content: "Inspect the current source state."}},
			}, nil)
			if response != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("Run error contract changed: response=%+v error=%v", response, err)
			}
			if len(observed) != 1 || math.Abs(observed[0].CostUSD-0.0003) > 1e-12 || observed[0].InputTokens != 100 {
				t.Fatalf("observer lost priced usage without durable store: %+v", observed)
			}
		})
	}
}

func TestRun_FailurePreservesUsageAndCompletedIteration(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			loop, store, archive := newAccountingTestLoop(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("stream disconnected")
			calls := 0
			loop.llm = accountingTestClient{call: func(callCtx context.Context, model string, _ []map[string]any) (*llm.ChatResponse, error) {
				calls++
				if calls == 1 {
					return accountingToolResponse(), nil
				}
				if canceled {
					cancel()
					failure = context.Canceled
					if !errors.Is(callCtx.Err(), context.Canceled) {
						t.Fatal("model call detached from caller cancellation")
					}
				}
				return &llm.ChatResponse{Model: model, UpstreamRequestID: "upstream-partial", InputTokens: 200, OutputTokens: 20}, failure
			}}
			multi := llm.NewMultiClient(nil)
			multi.AddProvider("primary", loop.llm)
			multi.AddRoute("claude-primary", "primary", "provider-primary")
			loop.llm = multi
			var live logging.RequestContent
			loop.liveRequestRecorder = func(_ context.Context, content logging.RequestContent) { live = content }
			retained := make(chan logging.RequestContent, 1)
			loop.requestRecorder = func(_ context.Context, content logging.RequestContent) { retained <- content }
			response, err := loop.Run(ctx, &Request{
				ConversationID: "failed-accounting", Model: "claude-primary",
				Messages: []Message{{Role: "user", Content: "Inspect the source before explaining it."}},
			}, nil)
			if response != nil || !errors.Is(err, failure) || calls != 2 {
				t.Fatalf("failure contract changed: response=%+v error=%v calls=%d", response, err, calls)
			}
			summary, err := loop.usageStore.Summary(time.Time{}, time.Now().Add(time.Hour))
			if err != nil || summary.TotalRecords != 2 || summary.TotalInputTokens != 300 || summary.TotalOutputTokens != 30 || math.Abs(summary.TotalCostUSD-0.0009) > 1e-12 {
				t.Fatalf("partial usage = %+v, error = %v", summary, err)
			}
			if live.InputTokens != 300 || live.OutputTokens != 30 || live.IterationCount != 1 {
				t.Fatalf("live partial trace = %+v", live)
			}
			select {
			case content := <-retained:
				if content.InputTokens != 300 || content.OutputTokens != 30 || content.IterationCount != 1 {
					t.Fatalf("retained partial trace = %+v", content)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("failed run did not retain request content")
			}
			sessionID := loop.archiver.ActiveSessionID("failed-accounting")
			iterations, err := archive.GetSessionIterations(sessionID)
			if err != nil || len(iterations) != 1 || len(iterations[0].ToolCallIDs) != 1 || iterations[0].InputTokens != 100 {
				t.Fatalf("completed partial iterations = %+v, error = %v", iterations, err)
			}
			var linkedInput int
			if err := store.DB().QueryRow(`SELECT ai.input_tokens FROM tool_calls tc JOIN archive_iterations ai ON ai.session_id = tc.session_id AND ai.iteration_index = tc.iteration_index WHERE tc.conversation_id = ?`, "failed-accounting").Scan(&linkedInput); err != nil || linkedInput != 100 {
				t.Fatalf("failed run lost tool/iteration link: input=%d error=%v", linkedInput, err)
			}
		})
	}
}

func TestRun_NoUsageForSyntheticRecovery(t *testing.T) {
	loop, _, _ := newAccountingTestLoop(t)
	loop.llm = accountingTestClient{call: func(context.Context, string, []map[string]any) (*llm.ChatResponse, error) {
		return nil, errors.New("request timeout")
	}}
	response, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "Explain the current state of the source."}},
	}, nil)
	if err != nil || response == nil || response.Content == "" {
		t.Fatalf("synthetic fallback = %+v, error = %v", response, err)
	}
	summary, err := loop.usageStore.Summary(time.Time{}, time.Now().Add(time.Hour))
	if err != nil || summary.TotalRecords != 0 || response.InputTokens != 0 || response.OutputTokens != 0 {
		t.Fatalf("fabricated usage: response=%+v summary=%+v error=%v", response, summary, err)
	}
}
