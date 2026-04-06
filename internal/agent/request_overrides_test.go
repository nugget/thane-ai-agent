package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/iterate"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestAllowedTools_RestrictsVisibleTools(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Done."},
				InputTokens:  42,
				OutputTokens: 7,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"alpha_tool", "beta_tool"})
	resp, err := loop.Run(context.Background(), &Request{
		Messages:     []Message{{Role: "user", Content: "use the allowed tool"}},
		AllowedTools: []string{"alpha_tool"},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if resp.Content != "Done." {
		t.Fatalf("Content = %q, want %q", resp.Content, "Done.")
	}
	if len(mock.calls) != 1 {
		t.Fatalf("mock call count = %d, want 1", len(mock.calls))
	}

	names := toolNames(mock.calls[0].Tools)
	if !hasName(names, "alpha_tool") {
		t.Fatalf("tools = %v, want alpha_tool present", names)
	}
	if hasName(names, "beta_tool") {
		t.Fatalf("tools = %v, want beta_tool filtered out", names)
	}
}

func TestExplicitSystemPrompt_ContextUsageUsesResolvedModel(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:        "explicit-model",
				Message:      llm.Message{Role: "assistant", Content: "Done."},
				InputTokens:  42,
				OutputTokens: 7,
			},
		},
	}

	loop := buildTestLoop(mock, nil)
	loop.model = "default-model"
	loop.contextWindow = 200000

	_, err := loop.Run(context.Background(), &Request{
		Model:        "explicit-model",
		SystemPrompt: "Delegate system prompt",
		Messages:     []Message{{Role: "user", Content: "say hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("mock call count = %d, want 1", len(mock.calls))
	}
	if len(mock.calls[0].Messages) == 0 {
		t.Fatal("expected system prompt message")
	}

	systemPrompt := mock.calls[0].Messages[0].Content
	if !strings.Contains(systemPrompt, "**Context:** explicit-model") {
		t.Fatalf("system prompt missing resolved model context line:\n%s", systemPrompt)
	}
	if strings.Contains(systemPrompt, "**Context:** default-model") {
		t.Fatalf("system prompt retained default model in context line:\n%s", systemPrompt)
	}
	if strings.Contains(systemPrompt, "(routed)") {
		t.Fatalf("system prompt should not mark explicit model as routed:\n%s", systemPrompt)
	}
}

func TestRun_ResponseIncludesIterationMetadata(t *testing.T) {
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
							Name:      "alpha_tool",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 10,
			},
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Finished."},
				InputTokens:  120,
				OutputTokens: 15,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"alpha_tool"})
	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "finish the task"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if resp.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", resp.Iterations)
	}
	if resp.Exhausted {
		t.Fatal("Exhausted = true, want false")
	}
	if resp.Model != "test-model" {
		t.Fatalf("Model = %q, want %q", resp.Model, "test-model")
	}
}

func TestMaxOutputTokens_StopsAfterBudget(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Budget hit."},
				InputTokens:  40,
				OutputTokens: 25,
			},
		},
	}

	loop := buildTestLoop(mock, nil)
	resp, err := loop.Run(context.Background(), &Request{
		Messages:        []Message{{Role: "user", Content: "keep it short"}},
		MaxOutputTokens: 20,
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("mock call count = %d, want 1", len(mock.calls))
	}
	if !resp.Exhausted {
		t.Fatal("Exhausted = false, want true")
	}
	if resp.FinishReason != iterate.ExhaustTokenBudget {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, iterate.ExhaustTokenBudget)
	}
	if resp.Content != "Budget hit." {
		t.Fatalf("Content = %q, want %q", resp.Content, "Budget hit.")
	}
}

func TestToolTimeout_CancelsToolExecution(t *testing.T) {
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
							Name:      "slow_tool",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  20,
				OutputTokens: 5,
			},
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Recovered."},
				InputTokens:  30,
				OutputTokens: 7,
			},
		},
	}

	timeoutCh := make(chan error, 1)
	reg := tools.NewRegistry(nil, nil)
	reg.Register(&tools.Tool{
		Name:        "slow_tool",
		Description: "slow tool",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			<-ctx.Done()
			timeoutCh <- ctx.Err()
			return "", ctx.Err()
		},
	})

	loop := &Loop{
		logger: slog.Default(),
		memory: newMockMem(),
		llm:    mock,
		tools:  reg,
		model:  "test-model",
	}

	resp, err := loop.Run(context.Background(), &Request{
		Messages:      []Message{{Role: "user", Content: "call the slow tool"}},
		ToolTimeout:   10 * time.Millisecond,
		MaxIterations: 3,
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if resp.Content != "Recovered." {
		t.Fatalf("Content = %q, want %q", resp.Content, "Recovered.")
	}

	select {
	case got := <-timeoutCh:
		if !errors.Is(got, context.DeadlineExceeded) {
			t.Fatalf("tool ctx err = %v, want deadline exceeded", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for slow tool cancellation")
	}

	if len(mock.calls) != 2 {
		t.Fatalf("mock call count = %d, want 2", len(mock.calls))
	}
	msgs := mock.calls[1].Messages
	if len(msgs) == 0 {
		t.Fatal("second call messages = empty, want tool error context")
	}
	last := msgs[len(msgs)-1]
	if last.Role != "tool" {
		t.Fatalf("last message role = %q, want tool", last.Role)
	}
	if !strings.Contains(last.Content, context.DeadlineExceeded.Error()) {
		t.Fatalf("tool error content = %q, want deadline exceeded", last.Content)
	}
}
