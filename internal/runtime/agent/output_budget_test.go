package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	"github.com/nugget/thane-ai-agent/internal/runtime/iterate"
)

func TestRun_OutputBudgetAcrossModelAttempts(t *testing.T) {
	type attempt struct {
		output   int
		timeout  bool
		noUsage  bool
		tool     bool
		textOnly bool // Recovery must not receive tool definitions.
		recovery bool
	}
	tests := []struct {
		name          string
		budget        int
		callCeiling   int
		maxIterations int
		attempts      []attempt
		wantCeilings  []int
		wantOutput    int
		wantRecords   int
		wantTools     int
		wantFinish    string
		wantExhausted bool
	}{
		{
			name: "retry spends only what the failed attempt left", budget: 1000,
			attempts:     []attempt{{output: 600, timeout: true}, {output: 300}},
			wantCeilings: []int{1000, 400}, wantOutput: 900, wantRecords: 2, wantFinish: "stop",
		},
		{
			name: "inherited call ceiling remains tighter than run allowance", budget: 1000, callCeiling: 300,
			attempts:     []attempt{{output: 200, timeout: true}, {output: 100}},
			wantCeilings: []int{300, 300}, wantOutput: 300, wantRecords: 2, wantFinish: "stop",
		},
		{
			name: "exhausted first attempt is not retried", budget: 1000,
			attempts:     []attempt{{output: 1000, timeout: true}},
			wantCeilings: []int{1000}, wantOutput: 1000, wantRecords: 1,
			wantFinish: iterate.ExhaustTokenBudget, wantExhausted: true,
		},
		{
			name: "exhaustion after tool work skips retry and recovery", budget: 1000,
			attempts:     []attempt{{output: 100, tool: true}, {output: 900, timeout: true}},
			wantCeilings: []int{1000, 900}, wantOutput: 1000, wantRecords: 2, wantTools: 1,
			wantFinish: iterate.ExhaustTokenBudget, wantExhausted: true,
		},
		{
			name: "retry exhaustion skips further retries and recovery", budget: 1000,
			attempts:     []attempt{{output: 100, tool: true}, {output: 300, timeout: true}, {output: 600, timeout: true}},
			wantCeilings: []int{1000, 900, 600}, wantOutput: 1000, wantRecords: 3, wantTools: 1,
			wantFinish: iterate.ExhaustTokenBudget, wantExhausted: true,
		},
		{
			name: "successful retry exhausts budget before new tool execution", budget: 1000,
			attempts:     []attempt{{output: 100, tool: true}, {output: 300, timeout: true}, {output: 600, tool: true}},
			wantCeilings: []int{1000, 900, 600}, wantOutput: 1000, wantRecords: 3, wantTools: 1,
			wantFinish: iterate.ExhaustTokenBudget, wantExhausted: true,
		},
		{
			name: "later iterations and forced text share failed attempt spend", budget: 1000, maxIterations: 2,
			attempts:     []attempt{{output: 200, timeout: true}, {output: 300, tool: true}, {output: 100, tool: true}, {output: 100, textOnly: true}},
			wantCeilings: []int{1000, 800, 500, 400}, wantOutput: 700, wantRecords: 4, wantTools: 2,
			wantFinish: iterate.ExhaustMaxIterations, wantExhausted: true,
		},
		{
			name: "timeout downshift shares every attempt's spend", budget: 1000,
			attempts:     []attempt{{output: 100, tool: true}, {output: 300, timeout: true}, {output: 100, timeout: true}, {output: 100, timeout: true}, {output: 100, textOnly: true, recovery: true}},
			wantCeilings: []int{1000, 900, 600, 500, 400}, wantOutput: 700, wantRecords: 5, wantTools: 1,
			wantFinish: "timeout_recovery",
		},
		{
			name: "failed timeout downshift spends final allowance", budget: 1000,
			attempts:     []attempt{{output: 100, tool: true}, {output: 300, timeout: true}, {output: 100, timeout: true}, {output: 100, timeout: true}, {output: 400, timeout: true, textOnly: true, recovery: true}},
			wantCeilings: []int{1000, 900, 600, 500, 400}, wantOutput: 1000, wantRecords: 5, wantTools: 1,
			wantFinish: iterate.ExhaustTokenBudget, wantExhausted: true,
		},
		{
			name: "successful timeout downshift spends final allowance", budget: 1000,
			attempts:     []attempt{{output: 100, tool: true}, {output: 300, timeout: true}, {output: 100, timeout: true}, {output: 100, timeout: true}, {output: 400, textOnly: true, recovery: true}},
			wantCeilings: []int{1000, 900, 600, 500, 400}, wantOutput: 1000, wantRecords: 5, wantTools: 1,
			wantFinish: iterate.ExhaustTokenBudget, wantExhausted: true,
		},
		{
			name: "failed forced text spends final allowance", budget: 1000, maxIterations: 1,
			attempts:     []attempt{{output: 200, timeout: true}, {output: 300, tool: true}, {output: 500, timeout: true, textOnly: true}},
			wantCeilings: []int{1000, 800, 500}, wantOutput: 1000, wantRecords: 3, wantTools: 1,
			wantFinish: iterate.ExhaustTokenBudget, wantExhausted: true,
		},
		{
			name: "successful forced text spends final allowance", budget: 1000, maxIterations: 1,
			attempts:     []attempt{{output: 200, timeout: true}, {output: 300, tool: true}, {output: 500, textOnly: true}},
			wantCeilings: []int{1000, 800, 500}, wantOutput: 1000, wantRecords: 3, wantTools: 1,
			wantFinish: iterate.ExhaustTokenBudget, wantExhausted: true,
		},
		{
			name: "unreported usage does not consume allowance", budget: 1000,
			attempts:     []attempt{{timeout: true, noUsage: true}, {output: 300}},
			wantCeilings: []int{1000, 1000}, wantOutput: 300, wantRecords: 1, wantFinish: "stop",
		},
		{
			name:         "zero remains unlimited",
			attempts:     []attempt{{output: 600, timeout: true}, {output: 300}},
			wantCeilings: []int{0, 0}, wantOutput: 900, wantRecords: 2, wantFinish: "stop",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			loop, store, _ := newAccountingTestLoop(t)
			loop.recoveryModel = "claude-recovery"
			var ceilings []int
			loop.llm = accountingTestClient{call: func(ctx context.Context, model string, defs []map[string]any) (*llm.ChatResponse, error) {
				i := len(ceilings)
				ceilings = append(ceilings, llm.MaxOutputTokensFromContext(ctx))
				if i >= len(tt.attempts) {
					return nil, fmt.Errorf("unexpected provider attempt %d", i+1)
				}
				step := tt.attempts[i]
				wantModel := "claude-primary"
				if step.recovery {
					wantModel = "claude-recovery"
				}
				if model != wantModel || ctx.Err() != nil {
					t.Errorf("attempt %d: model=%q context error=%v, want live %q call", i+1, model, ctx.Err(), wantModel)
				}
				if step.textOnly && len(defs) != 0 {
					t.Errorf("recovery attempt %d received %d tools", i+1, len(defs))
				}
				var response *llm.ChatResponse
				if !step.noUsage {
					response = &llm.ChatResponse{
						Model: model, UpstreamRequestID: fmt.Sprintf("attempt-%d", i+1),
						InputTokens: 100, OutputTokens: step.output, Done: !step.timeout,
					}
				}
				if step.timeout {
					return response, errors.New("stream timeout after reported usage")
				}
				response.Message = llm.Message{Role: "assistant", Content: "Source inspection complete."}
				if step.tool {
					response.Message = accountingToolResponse().Message
					response.Message.ToolCalls[0].ID = fmt.Sprintf("source-call-%d", i+1)
				}
				return response, nil
			}}
			var observed []usage.Record
			ctx := usage.WithObserver(context.Background(), func(record usage.Record) { observed = append(observed, record) })
			if tt.callCeiling > 0 {
				ctx = llm.WithMaxOutputTokens(ctx, tt.callCeiling)
			}
			const conversationID = "output-budget"
			response, err := loop.Run(ctx, &Request{
				ConversationID: conversationID, Model: "claude-primary",
				Messages:        []Message{{Role: "user", Content: "Inspect the source and explain what you find."}},
				MaxOutputTokens: tt.budget, MaxIterations: tt.maxIterations,
			}, nil)
			if !slices.Equal(ceilings, tt.wantCeilings) {
				t.Errorf("provider output ceilings = %v, want %v", ceilings, tt.wantCeilings)
			}
			if err != nil || response == nil {
				t.Fatalf("Run response = %+v, error = %v", response, err)
			}
			if response.Exhausted != tt.wantExhausted || response.FinishReason != tt.wantFinish {
				t.Errorf("finish = %q exhausted=%t, want %q exhausted=%t", response.FinishReason, response.Exhausted, tt.wantFinish, tt.wantExhausted)
			}
			if response.Content == "" {
				t.Error("run returned no completed response or exhaustion fallback")
			}
			lastAttempt := tt.attempts[len(tt.attempts)-1]
			if !lastAttempt.timeout && !lastAttempt.tool && response.Content != "Source inspection complete." {
				t.Errorf("completed text was lost: %q", response.Content)
			}
			if response.OutputTokens != tt.wantOutput || response.InputTokens != tt.wantRecords*100 {
				t.Errorf("response usage = %d/%d, want %d/%d", response.InputTokens, response.OutputTokens, tt.wantRecords*100, tt.wantOutput)
			}
			if response.ToolsUsed["inspect"] != tt.wantTools {
				t.Errorf("completed inspections = %d, want %d", response.ToolsUsed["inspect"], tt.wantTools)
			}
			summary, err := loop.usageStore.Summary(time.Time{}, time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if summary.TotalRecords != tt.wantRecords || summary.TotalOutputTokens != int64(tt.wantOutput) || summary.TotalInputTokens != int64(tt.wantRecords*100) || len(observed) != tt.wantRecords {
				t.Fatalf("provider usage was lost or counted twice: ledger=%+v observer records=%d", summary, len(observed))
			}
			recordIndex := 0
			for i, step := range tt.attempts {
				if step.noUsage {
					continue
				}
				upstream := fmt.Sprintf("attempt-%d", i+1)
				record := observed[recordIndex]
				if record.UpstreamRequestID != upstream || record.OutputTokens != step.output || record.InputTokens != 100 {
					t.Errorf("observer record %d = %+v, want attempt %q usage 100/%d", recordIndex, record, upstream, step.output)
				}
				var persisted int
				if err := store.DB().QueryRow(`SELECT COUNT(*) FROM usage_records WHERE upstream_request_id = ? AND model = ? AND output_tokens = ? AND input_tokens = ?`, upstream, record.Model, step.output, 100).Scan(&persisted); err != nil || persisted != 1 {
					t.Errorf("durable usage for %s: matching records=%d error=%v, want one", upstream, persisted, err)
				}
				recordIndex++
			}
			var completedTools int
			if err := store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls tc JOIN archive_iterations ai ON ai.session_id = tc.session_id AND ai.iteration_index = tc.iteration_index WHERE tc.conversation_id = ? AND tc.result = 'source evidence'`, conversationID).Scan(&completedTools); err != nil || completedTools != tt.wantTools {
				t.Errorf("preserved tool results linked to iterations = %d, want %d; error=%v", completedTools, tt.wantTools, err)
			}
		})
	}
}

func TestRun_OutputBudgetPreservesCancellation(t *testing.T) {
	loop, _, _ := newAccountingTestLoop(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var observed []usage.Record
	ctx = usage.WithObserver(ctx, func(record usage.Record) { observed = append(observed, record) })
	calls := 0
	loop.llm = accountingTestClient{call: func(callCtx context.Context, model string, _ []map[string]any) (*llm.ChatResponse, error) {
		calls++
		if got := llm.MaxOutputTokensFromContext(callCtx); got != 100 {
			t.Errorf("provider output ceiling = %d, want 100", got)
		}
		cancel()
		return &llm.ChatResponse{Model: model, InputTokens: 50, OutputTokens: 100}, context.Canceled
	}}
	response, err := loop.Run(ctx, &Request{
		Model: "claude-primary", MaxOutputTokens: 100,
		Messages: []Message{{Role: "user", Content: "Inspect the source."}},
	}, nil)
	if response != nil || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancellation replaced by budget exhaustion: response=%+v error=%v calls=%d", response, err, calls)
	}
	summary, err := loop.usageStore.Summary(time.Time{}, time.Now().Add(time.Hour))
	if err != nil || summary.TotalRecords != 1 || summary.TotalInputTokens != 50 || summary.TotalOutputTokens != 100 {
		t.Fatalf("cancellation lost final reported usage: summary=%+v error=%v", summary, err)
	}
	if len(observed) != 1 || observed[0].InputTokens != 50 || observed[0].OutputTokens != 100 {
		t.Fatalf("cancellation lost observed usage: %+v", observed)
	}
}
