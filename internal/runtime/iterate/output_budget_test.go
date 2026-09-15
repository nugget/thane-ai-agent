package iterate

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
)

func TestEngine_OutputLimitStopsToolsWithoutBudgetCallback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		priorTool bool
		output    int
		textOnly  bool
	}{
		{"exact first tool response", false, 100, false},
		{"overspent first tool response", false, 101, false},
		{"exact after completed tool", true, 90, false},
		{"overspent after completed tool", true, 91, false},
		{"exact text completion", true, 90, true},
		{"overspent text completion", true, 91, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			completed := completedBudgetToolResponse()
			last := toolCallResponse(makeToolCall("never_execute", nil))
			last.Message.Content = "last model text"
			last.OutputTokens = tc.output
			last.UpstreamRequestID = "budget-boundary-request"
			if tc.textOnly {
				last.Message.ToolCalls = nil
			}
			client := &mockLLM{}
			wantMessages := baseMessages()
			priorCount := 0
			if tc.priorTool {
				priorCount = 1
				client.responses = append(client.responses, completed)
				wantMessages = append(wantMessages, completed.Message, llm.Message{Role: "tool", Content: "saved result", ToolCallID: "completed-call"})
			}
			client.responses = append(client.responses, last)
			executor := &mockExecutor{results: map[string]string{"search": "saved result"}}
			cfg := baseCfg(client, executor)
			cfg.MaxOutputTokens = 100
			cfg.MaxIterations = 4
			cfg.FallbackContent = "budget spent"
			// Deliberately leave CheckBudget nil: the configured limit must
			// stop tools even when the caller supplies no budget callback.
			var toolStarts int
			cfg.OnToolCallStart = func(context.Context, llm.ToolCall) { toolStarts++ }
			result, err := (&Engine{}).Run(context.Background(), cfg, baseMessages())
			if err != nil {
				t.Fatal(err)
			}
			if !result.Exhausted || result.ExhaustReason != ExhaustTokenBudget {
				t.Fatalf("exhaustion = %t/%q, want token budget", result.Exhausted, result.ExhaustReason)
			}
			if client.callIdx != priorCount+1 || len(executor.calls) != priorCount || toolStarts != priorCount {
				t.Fatalf("work continued after budget boundary: model calls=%d, tools=%v, starts=%d", client.callIdx, executor.calls, toolStarts)
			}
			wantContent := cfg.FallbackContent
			if tc.textOnly {
				wantContent = last.Message.Content
				wantMessages = append(wantMessages, last.Message)
			}
			if result.Content != wantContent || !reflect.DeepEqual(result.Messages, wantMessages) {
				t.Fatalf("final output/history changed: content=%q, messages=%+v", result.Content, result.Messages)
			}
			if result.OutputTokens != tc.output+priorCount*completed.OutputTokens || result.IterationCount != priorCount+1 || len(result.Iterations) != priorCount+1 {
				t.Fatalf("successful boundary response lost from accounting: %+v", result)
			}
			record := result.Iterations[priorCount]
			if record.BreakReason != ExhaustTokenBudget || record.OutputTokens != tc.output || record.UpstreamRequestID != last.UpstreamRequestID || len(record.ToolCallIDs) != 0 {
				t.Fatalf("boundary iteration = %+v", record)
			}
			if result.ToolsUsed["search"] != priorCount || result.ToolOutcomes["search"].Successes != priorCount || result.ToolsUsed["never_execute"] != 0 {
				t.Fatalf("completed tool accounting changed: uses=%v, outcomes=%v", result.ToolsUsed, result.ToolOutcomes)
			}
		})
	}
}

// outputBudgetClient can return both an error and a partial response. The
// ordinary mockLLM discards the response when its scripted error is non-nil.
type outputBudgetClient func(context.Context, []map[string]any) (*llm.ChatResponse, error)

func (f outputBudgetClient) Chat(ctx context.Context, model string, messages []llm.Message, defs []map[string]any) (*llm.ChatResponse, error) {
	return f.ChatStream(ctx, model, messages, defs, nil)
}

func (f outputBudgetClient) ChatStream(ctx context.Context, _ string, _ []llm.Message, defs []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	return f(ctx, defs)
}

func (f outputBudgetClient) Ping(context.Context) error { return nil }

func TestEngine_OutputBudgetErrorPreservesCompletedWork(t *testing.T) {
	for _, tc := range []struct {
		name        string
		priorTool   bool
		errorHook   bool
		forceText   bool
		defaultText bool
	}{
		{"first call", false, false, false, false},
		{"first call recovery", false, true, false, true},
		{"after completed tool", true, false, false, false},
		{"recovery after completed tool", true, true, false, false},
		{"force text after completed tool", true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			completed := completedBudgetToolResponse()
			failed := toolCallResponse(makeToolCall("never_execute", nil))
			failed.Model = "failed-model"
			failed.UpstreamRequestID = "failed-request"
			failed.Message.Content = "untrusted partial output"
			failed.OutputTokens = 90
			budgetErr := fmt.Errorf("shared call allowance: %w", llm.ErrOutputBudgetExhausted)
			upstreamErr := errors.New("provider failed before recovery")
			calls, hooks, responseCallbacks, toolStarts := 0, 0, 0, 0
			priorCount := 0
			wantMessages := baseMessages()
			if tc.priorTool {
				priorCount = 1
				wantMessages = append(wantMessages, completed.Message, llm.Message{Role: "tool", Content: "saved result", ToolCallID: "completed-call"})
			}
			executor := &mockExecutor{results: map[string]string{"search": "saved result"}}
			cfg := baseCfg(nil, executor)
			cfg.MaxIterations = 4
			cfg.MaxOutputTokens = 100
			cfg.FallbackContent = "budget spent"
			if tc.defaultText {
				cfg.FallbackContent = ""
			}
			if tc.forceText {
				cfg.MaxIterations = 1
			}
			cfg.LLM = outputBudgetClient(func(ctx context.Context, defs []map[string]any) (*llm.ChatResponse, error) {
				calls++
				if calls > priorCount+1 {
					t.Fatal("model called again after budget exhaustion")
				}
				if tc.priorTool && calls == 1 {
					return completed, nil
				}
				if got := llm.MaxOutputTokensFromContext(ctx); got != 100-priorCount*completed.OutputTokens {
					t.Fatalf("remaining call allowance = %d", got)
				}
				if tc.forceText && len(defs) != 0 {
					t.Fatal("force-text recovery offered tools")
				}
				if tc.errorHook {
					return failed, upstreamErr
				}
				return failed, budgetErr
			})
			cfg.OnLLMError = func(_ context.Context, err error, _ string, _ []llm.Message, _ []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, string, error) {
				hooks++
				if !tc.errorHook || !errors.Is(err, upstreamErr) {
					t.Fatalf("budget error incorrectly sent to recovery: %v", err)
				}
				return failed, "failed-model", budgetErr
			}
			cfg.OnLLMResponse = func(context.Context, *llm.ChatResponse, int) { responseCallbacks++ }
			cfg.OnToolCallStart = func(context.Context, llm.ToolCall) { toolStarts++ }
			result, err := (&Engine{}).Run(context.Background(), cfg, baseMessages())
			if err != nil {
				t.Fatalf("budget exhaustion escaped as an execution error: %v", err)
			}
			wantContent := cfg.FallbackContent
			if wantContent == "" {
				wantContent = prompts.EmptyResponseFallback
			}
			if !result.Exhausted || result.ExhaustReason != ExhaustTokenBudget || result.Content != wantContent {
				t.Fatalf("budget result = %+v", result)
			}
			wantHooks := 0
			if tc.errorHook {
				wantHooks = 1
			}
			if calls != priorCount+1 || hooks != wantHooks || responseCallbacks != priorCount || toolStarts != priorCount || len(executor.calls) != priorCount {
				t.Fatalf("failed output produced work: calls=%d, hooks=%d, responses=%d, starts=%d, tools=%v", calls, hooks, responseCallbacks, toolStarts, executor.calls)
			}
			if !reflect.DeepEqual(result.Messages, wantMessages) || result.Model != cfg.Model {
				t.Fatalf("failed response changed history or model: %+v", result)
			}
			if result.IterationCount != priorCount || len(result.Iterations) != priorCount || result.InputTokens != priorCount*completed.InputTokens || result.OutputTokens != priorCount*completed.OutputTokens {
				t.Fatalf("failed response became a completed iteration: %+v", result)
			}
			if result.ToolsUsed["search"] != priorCount || result.ToolOutcomes["search"].Successes != priorCount || result.ToolsUsed["never_execute"] != 0 {
				t.Fatalf("completed tool outcome lost: uses=%v, outcomes=%v", result.ToolsUsed, result.ToolOutcomes)
			}
			if tc.priorTool {
				record := result.Iterations[0]
				if result.UpstreamRequestID != completed.UpstreamRequestID || record.UpstreamRequestID != completed.UpstreamRequestID || !reflect.DeepEqual(record.ToolCallIDs, []string{"completed-call"}) {
					t.Fatalf("completed provenance lost: %+v", result)
				}
				if result.CacheCreationInputTokens != 5 || result.CacheCreation5mInputTokens != 2 || result.CacheCreation1hInputTokens != 3 || result.CacheReadInputTokens != 7 || result.PeakInputTokens != completed.InputTokens {
					t.Fatalf("completed usage lost: %+v", result)
				}
			}
		})
	}
}

func completedBudgetToolResponse() *llm.ChatResponse {
	response := toolCallResponse(makeToolCall("search", nil))
	response.Message.ToolCalls[0].ID = "completed-call"
	response.UpstreamRequestID = "completed-request"
	response.CacheCreationInputTokens = 5
	response.CacheCreation5mInputTokens = 2
	response.CacheCreation1hInputTokens = 3
	response.CacheReadInputTokens = 7
	return response
}
