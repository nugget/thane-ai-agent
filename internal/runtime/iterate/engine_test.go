package iterate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// mockLLM is a test double for llm.Client that returns pre-configured
// responses in sequence.
type mockLLM struct {
	mu        sync.Mutex
	responses []*llm.ChatResponse
	errors    []error
	callIdx   int
	calls     []mockLLMCall
}

type mockLLMCall struct {
	Model    string
	Messages []llm.Message
	Tools    []map[string]any
}

func (m *mockLLM) Chat(_ context.Context, model string, messages []llm.Message, tls []map[string]any) (*llm.ChatResponse, error) {
	return m.ChatStream(context.Background(), model, messages, tls, nil)
}

func (m *mockLLM) ChatStream(_ context.Context, model string, messages []llm.Message, tls []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.callIdx
	m.callIdx++
	m.calls = append(m.calls, mockLLMCall{Model: model, Messages: messages, Tools: tls})

	if idx < len(m.errors) && m.errors[idx] != nil {
		return nil, m.errors[idx]
	}
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	// Default: return empty text response.
	return &llm.ChatResponse{
		Model:   model,
		Message: llm.Message{Role: "assistant", Content: "default response"},
	}, nil
}

func (m *mockLLM) Ping(_ context.Context) error { return nil }

// mockExecutor records tool calls and returns configured results.
type mockExecutor struct {
	mu      sync.Mutex
	results map[string]string
	errors  map[string]error
	calls   []string
}

func (m *mockExecutor) Execute(_ context.Context, name, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, name)
	if err, ok := m.errors[name]; ok {
		return "", err
	}
	if r, ok := m.results[name]; ok {
		return r, nil
	}
	return "ok", nil
}

func textResponse(content string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Model:        "test-model",
		Message:      llm.Message{Role: "assistant", Content: content},
		InputTokens:  10,
		OutputTokens: 5,
	}
}

func toolCallResponse(calls ...llm.ToolCall) *llm.ChatResponse {
	return &llm.ChatResponse{
		Model: "test-model",
		Message: llm.Message{
			Role:      "assistant",
			ToolCalls: calls,
		},
		InputTokens:  20,
		OutputTokens: 10,
	}
}

func makeToolCall(name string, args map[string]any) llm.ToolCall {
	return llm.ToolCall{
		ID: "tc_" + name,
		Function: struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}{
			Name:      name,
			Arguments: args,
		},
	}
}

func baseCfg(mock *mockLLM, exec *mockExecutor) Config {
	return Config{
		Model: "test-model",
		LLM:   mock,
		ToolDefs: func(int) []map[string]any {
			return []map[string]any{
				{"type": "function", "function": map[string]any{"name": "search"}},
			}
		},
		Executor: exec,
	}
}

func baseMessages() []llm.Message {
	return []llm.Message{
		{Role: "system", Content: "You are a test assistant."},
		{Role: "user", Content: "Hello"},
	}
}

func TestEngine_TextOnlyResponse(t *testing.T) {
	mock := &mockLLM{responses: []*llm.ChatResponse{textResponse("Hello back!")}}
	exec := &mockExecutor{results: map[string]string{}}
	cfg := baseCfg(mock, exec)

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Hello back!" {
		t.Errorf("content = %q, want %q", result.Content, "Hello back!")
	}
	if result.Exhausted {
		t.Error("should not be exhausted")
	}
	if result.IterationCount != 1 {
		t.Errorf("iteration count = %d, want 1", result.IterationCount)
	}
	if result.InputTokens != 10 {
		t.Errorf("input tokens = %d, want 10", result.InputTokens)
	}
	if result.OutputTokens != 5 {
		t.Errorf("output tokens = %d, want 5", result.OutputTokens)
	}
	if len(exec.calls) != 0 {
		t.Errorf("no tools should have been called, got %v", exec.calls)
	}
}

func TestEngine_SingleToolCallThenText(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			toolCallResponse(makeToolCall("search", map[string]any{"q": "test"})),
			textResponse("Found results!"),
		},
	}
	exec := &mockExecutor{results: map[string]string{"search": "result data"}}
	cfg := baseCfg(mock, exec)

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Found results!" {
		t.Errorf("content = %q, want %q", result.Content, "Found results!")
	}
	if result.IterationCount != 2 {
		t.Errorf("iteration count = %d, want 2", result.IterationCount)
	}
	if result.ToolsUsed["search"] != 1 {
		t.Errorf("search tool use count = %d, want 1", result.ToolsUsed["search"])
	}
	if len(exec.calls) != 1 || exec.calls[0] != "search" {
		t.Errorf("executor calls = %v, want [search]", exec.calls)
	}
	// Verify tool result message was appended.
	if len(result.Messages) < 4 {
		t.Fatalf("expected at least 4 messages, got %d", len(result.Messages))
	}
	toolMsg := result.Messages[3] // system, user, assistant+tool_calls, tool result
	if toolMsg.Role != "tool" || toolMsg.Content != "result data" {
		t.Errorf("tool message = %+v, want role=tool content=result data", toolMsg)
	}
}

func TestEngine_MultipleToolCalls(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			toolCallResponse(
				makeToolCall("search", map[string]any{"q": "a"}),
				makeToolCall("read", map[string]any{"path": "/x"}),
			),
			textResponse("Done with both tools."),
		},
	}
	exec := &mockExecutor{
		results: map[string]string{"search": "found", "read": "contents"},
	}
	cfg := baseCfg(mock, exec)

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Done with both tools." {
		t.Errorf("content = %q", result.Content)
	}
	if result.ToolsUsed["search"] != 1 || result.ToolsUsed["read"] != 1 {
		t.Errorf("tools used = %v", result.ToolsUsed)
	}
	if len(exec.calls) != 2 {
		t.Errorf("executor calls = %v, want 2 calls", exec.calls)
	}
}

func TestEngine_NormalizeToolCallPersistsNormalizedMessage(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			toolCallResponse(makeToolCall("search_alias", map[string]any{"q": "test"})),
			textResponse("Done."),
		},
	}
	exec := &mockExecutor{results: map[string]string{"search": "result data"}}
	cfg := baseCfg(mock, exec)
	cfg.NormalizeToolCall = func(_ context.Context, _ int, tc llm.ToolCall) llm.ToolCall {
		tc.Function.Name = "search"
		return tc
	}

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exec.calls) != 1 || exec.calls[0] != "search" {
		t.Fatalf("executor calls = %v, want [search]", exec.calls)
	}
	if len(result.Messages) < 3 {
		t.Fatalf("expected at least 3 messages, got %d", len(result.Messages))
	}
	assistantMsg := result.Messages[2]
	if len(assistantMsg.ToolCalls) != 1 {
		t.Fatalf("assistant tool calls = %#v, want one tool call", assistantMsg.ToolCalls)
	}
	if assistantMsg.ToolCalls[0].Function.Name != "search" {
		t.Fatalf("stored tool name = %q, want normalized name search", assistantMsg.ToolCalls[0].Function.Name)
	}
}

func TestEngine_MaxIterationsExhaustion(t *testing.T) {
	// Model always returns tool calls, never text, for MaxIterations rounds.
	// Then the force-text call (iteration 3) returns text.
	responses := make([]*llm.ChatResponse, 3)
	for i := range responses {
		responses[i] = toolCallResponse(makeToolCall(
			fmt.Sprintf("tool_%d", i),
			map[string]any{"i": i},
		))
	}
	// Force text response after exhaustion (call index 3).
	responses = append(responses, textResponse("forced response"))

	mock := &mockLLM{responses: responses}
	exec := &mockExecutor{results: map[string]string{}}
	cfg := baseCfg(mock, exec)
	cfg.MaxIterations = 3

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Exhausted {
		t.Error("should be exhausted")
	}
	if result.ExhaustReason != ExhaustMaxIterations {
		t.Errorf("exhaust reason = %q, want %q", result.ExhaustReason, ExhaustMaxIterations)
	}
	if result.Content != "forced response" {
		t.Errorf("content = %q, want %q", result.Content, "forced response")
	}
}

func TestEngine_IllegalToolStrikeRecovery(t *testing.T) {
	// First call: model calls unavailable tool.
	// Second call: model calls unavailable tool again → breaks.
	// Force text after break.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			toolCallResponse(makeToolCall("banned_tool", nil)),
			toolCallResponse(makeToolCall("banned_tool", nil)),
			textResponse("forced after illegal"),
		},
	}
	exec := &mockExecutor{results: map[string]string{}}
	cfg := baseCfg(mock, exec)
	cfg.CheckToolAvail = func(name string) bool { return name != "banned_tool" }

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Exhausted {
		t.Error("should be exhausted due to illegal tool")
	}
	if result.ExhaustReason != ExhaustIllegalTool {
		t.Errorf("exhaust reason = %q, want %q", result.ExhaustReason, ExhaustIllegalTool)
	}
	if result.Content != "forced after illegal" {
		t.Errorf("content = %q", result.Content)
	}
}

func TestEngine_IllegalToolSingleStrikeRecovery(t *testing.T) {
	// First call: model calls unavailable tool (strike 1).
	// Second call: model recovers with text.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			toolCallResponse(makeToolCall("banned_tool", nil)),
			textResponse("recovered with text"),
		},
	}
	exec := &mockExecutor{results: map[string]string{}}
	cfg := baseCfg(mock, exec)
	cfg.CheckToolAvail = func(name string) bool { return name != "banned_tool" }

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Exhausted {
		t.Error("should not be exhausted — model recovered with text")
	}
	if result.Content != "recovered with text" {
		t.Errorf("content = %q", result.Content)
	}
}

func TestEngine_ToolLoopDetection(t *testing.T) {
	// Model calls same tool with same args 4 times (> MaxToolRepeat of 3).
	sameCall := makeToolCall("search", map[string]any{"q": "same"})
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			toolCallResponse(sameCall),
			toolCallResponse(sameCall),
			toolCallResponse(sameCall),
			toolCallResponse(sameCall), // This triggers loop detection.
			textResponse("broke out of loop"),
		},
	}
	exec := &mockExecutor{results: map[string]string{"search": "found"}}
	cfg := baseCfg(mock, exec)

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should get a response (the loop detection injects an error and
	// continues; the model eventually responds with text).
	if result.Content != "broke out of loop" {
		t.Errorf("content = %q", result.Content)
	}

	// Verify the loop detection fired on the 4th call (count > 3).
	loopDetected := false
	for _, iter := range result.Iterations {
		if iter.BreakReason == "tool_loop" {
			loopDetected = true
			break
		}
	}
	if !loopDetected {
		t.Error("expected tool_loop break reason in iterations")
	}
}

func TestEngine_EmptyResponseNudge(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			toolCallResponse(makeToolCall("work", nil)),
			textResponse(""),                // Empty after tools.
			textResponse("nudged response"), // After nudge.
		},
	}
	exec := &mockExecutor{results: map[string]string{"work": "done"}}
	cfg := baseCfg(mock, exec)
	cfg.NudgeOnEmpty = true

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "nudged response" {
		t.Errorf("content = %q, want %q", result.Content, "nudged response")
	}
}

func TestEngine_EmptyResponseFallback(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			toolCallResponse(makeToolCall("work", nil)),
			textResponse(""), // Empty after tools.
			textResponse(""), // Empty after nudge.
		},
	}
	exec := &mockExecutor{results: map[string]string{"work": "done"}}
	cfg := baseCfg(mock, exec)
	cfg.NudgeOnEmpty = true
	cfg.FallbackContent = "custom fallback"

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "custom fallback" {
		t.Errorf("content = %q, want %q", result.Content, "custom fallback")
	}
}

func TestEngine_EmptyResponseWithoutNudge(t *testing.T) {
	// When NudgeOnEmpty is false, empty response after tools is preserved
	// as empty — the caller decides how to handle it (e.g., delegate
	// treats it as ExhaustNoOutput).
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			toolCallResponse(makeToolCall("work", nil)),
			textResponse(""), // Empty after tools.
		},
	}
	exec := &mockExecutor{results: map[string]string{"work": "done"}}
	cfg := baseCfg(mock, exec)
	cfg.NudgeOnEmpty = false // No nudge.

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "" {
		t.Errorf("content = %q, want empty (caller handles no-output)", result.Content)
	}
}

func TestEngine_EmptyFirstResponseNudges(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			textResponse(""),
			textResponse("nudged response"),
		},
	}
	exec := &mockExecutor{results: map[string]string{}}
	cfg := baseCfg(mock, exec)
	cfg.NudgeOnEmpty = true

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "nudged response" {
		t.Errorf("content = %q, want %q", result.Content, "nudged response")
	}
}

func TestEngine_DeferMixedText(t *testing.T) {
	// Model returns text + tool calls, then empty response.
	// Deferred text should be used.
	resp := &llm.ChatResponse{
		Model: "test-model",
		Message: llm.Message{
			Role:    "assistant",
			Content: "I'll search for that.",
			ToolCalls: []llm.ToolCall{
				makeToolCall("search", map[string]any{"q": "test"}),
			},
		},
		InputTokens:  20,
		OutputTokens: 10,
	}
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			resp,
			textResponse(""), // Empty — should use deferred text.
		},
	}
	exec := &mockExecutor{results: map[string]string{"search": "found"}}
	cfg := baseCfg(mock, exec)
	cfg.DeferMixedText = true

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "I'll search for that." {
		t.Errorf("content = %q, want deferred text", result.Content)
	}
}

func TestEngine_BudgetExhaustion(t *testing.T) {
	// Budget fires on the second iteration (after iter 0's tool has been
	// executed, so the last message is a tool result). This lets forceText
	// fire a real LLM call to produce a text response.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			toolCallResponse(makeToolCall("work", nil)), // iter 0: tool call (10 tokens)
			toolCallResponse(makeToolCall("work", nil)), // iter 1: tool call → budget fires (20 >= 20)
			textResponse("forced by budget"),            // forceText LLM call
		},
	}
	exec := &mockExecutor{results: map[string]string{"work": "done"}}
	cfg := baseCfg(mock, exec)
	// Budget fires when cumulative output tokens reach 20 (after 2 iterations).
	cfg.CheckBudget = func(totalOutput int) bool { return totalOutput >= 20 }

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Exhausted {
		t.Error("should be exhausted")
	}
	if result.ExhaustReason != ExhaustTokenBudget {
		t.Errorf("exhaust reason = %q", result.ExhaustReason)
	}
	if result.Content != "forced by budget" {
		t.Errorf("content = %q, want %q", result.Content, "forced by budget")
	}
	// forceText should have made a third LLM call (with tools=nil).
	if mock.callIdx != 3 {
		t.Errorf("LLM called %d times, want 3", mock.callIdx)
	}
	if mock.calls[2].Tools != nil {
		t.Error("forceText call should have tools=nil")
	}
}

func TestEngine_OnLLMErrorRecovery(t *testing.T) {
	timeoutErr := errors.New("timeout")
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			nil, // First call errors.
			textResponse("recovered"),
		},
		errors: []error{timeoutErr, nil},
	}
	exec := &mockExecutor{results: map[string]string{}}
	cfg := baseCfg(mock, exec)
	cfg.OnLLMError = func(ctx context.Context, err error, model string,
		msgs []llm.Message, toolDefs []map[string]any,
		stream llm.StreamCallback) (*llm.ChatResponse, string, error) {
		// Simulate retry by calling LLM again.
		resp, retryErr := mock.ChatStream(ctx, model, msgs, toolDefs, stream)
		return resp, model, retryErr
	}

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "recovered" {
		t.Errorf("content = %q", result.Content)
	}
}

func TestEngine_OnLLMErrorPropagation(t *testing.T) {
	llmErr := errors.New("permanent failure")
	mock := &mockLLM{errors: []error{llmErr}}
	exec := &mockExecutor{results: map[string]string{}}
	cfg := baseCfg(mock, exec)
	// No OnLLMError handler — error propagates.

	engine := &Engine{}
	_, err := engine.Run(context.Background(), cfg, baseMessages())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "permanent failure") {
		t.Errorf("error = %v, want permanent failure", err)
	}
}

func TestEngine_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	mock := &mockLLM{errors: []error{context.Canceled}}
	exec := &mockExecutor{results: map[string]string{}}
	cfg := baseCfg(mock, exec)

	engine := &Engine{}
	_, err := engine.Run(ctx, cfg, baseMessages())
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestEngine_ToolExecError(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			toolCallResponse(makeToolCall("flaky", nil)),
			textResponse("handled error"),
		},
	}
	exec := &mockExecutor{
		results: map[string]string{},
		errors:  map[string]error{"flaky": errors.New("connection refused")},
	}
	cfg := baseCfg(mock, exec)

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "handled error" {
		t.Errorf("content = %q", result.Content)
	}
	// Verify error message was passed to LLM.
	if len(mock.calls) < 2 {
		t.Fatal("expected at least 2 LLM calls")
	}
	lastMsgs := mock.calls[1].Messages
	toolResultMsg := lastMsgs[len(lastMsgs)-1]
	if !strings.Contains(toolResultMsg.Content, "Error: connection refused") {
		t.Errorf("tool result = %q, want error content", toolResultMsg.Content)
	}
}

func TestEngine_CallbacksFired(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			toolCallResponse(makeToolCall("ping", nil)),
			textResponse("pong"),
		},
	}
	exec := &mockExecutor{results: map[string]string{"ping": "alive"}}
	cfg := baseCfg(mock, exec)

	var (
		iterStartCalled    int
		llmResponseCalled  int
		toolCallStartCalls []string
		toolCallDoneCalls  []string
		textResponseCalled bool
	)

	cfg.OnIterationStart = func(_ context.Context, _ int, _ string, _ []llm.Message, _ []map[string]any) { iterStartCalled++ }
	cfg.OnLLMResponse = func(_ context.Context, _ *llm.ChatResponse, _ int) { llmResponseCalled++ }
	cfg.OnToolCallStart = func(_ context.Context, tc llm.ToolCall) {
		toolCallStartCalls = append(toolCallStartCalls, tc.Function.Name)
	}
	cfg.OnToolCallDone = func(_ context.Context, name, _, _ string) {
		toolCallDoneCalls = append(toolCallDoneCalls, name)
	}
	cfg.OnTextResponse = func(_ context.Context, _ string, _ []llm.Message) {
		textResponseCalled = true
	}

	engine := &Engine{}
	_, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if iterStartCalled != 2 {
		t.Errorf("OnIterationStart called %d times, want 2", iterStartCalled)
	}
	if llmResponseCalled != 2 {
		t.Errorf("OnLLMResponse called %d times, want 2", llmResponseCalled)
	}
	if len(toolCallStartCalls) != 1 || toolCallStartCalls[0] != "ping" {
		t.Errorf("OnToolCallStart calls = %v, want [ping]", toolCallStartCalls)
	}
	if len(toolCallDoneCalls) != 1 || toolCallDoneCalls[0] != "ping" {
		t.Errorf("OnToolCallDone calls = %v, want [ping]", toolCallDoneCalls)
	}
	if !textResponseCalled {
		t.Error("OnTextResponse not called")
	}
}

func TestEngine_OnBeforeToolExec(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			toolCallResponse(makeToolCall("work", nil)),
			textResponse("done"),
		},
	}

	// The executor checks for a context value set by OnBeforeToolExec.
	type ctxKey struct{}
	var gotValue string
	exec := &mockExecutor{results: map[string]string{"work": "ok"}}

	cfg := baseCfg(mock, exec)
	cfg.OnBeforeToolExec = func(ctx context.Context, _ int, _ llm.ToolCall) context.Context {
		return context.WithValue(ctx, ctxKey{}, "enriched")
	}
	// Replace executor to check context.
	cfg.Executor = &DirectExecutor{
		Exec: func(ctx context.Context, name, argsJSON string) (string, error) {
			if v, ok := ctx.Value(ctxKey{}).(string); ok {
				gotValue = v
			}
			return "ok", nil
		},
	}

	engine := &Engine{}
	_, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotValue != "enriched" {
		t.Errorf("context value = %q, want %q", gotValue, "enriched")
	}
}

func TestEngine_ErrToolUnavailableFromExecutor(t *testing.T) {
	// When the executor returns ErrToolUnavailable (not via CheckToolAvail),
	// the engine should treat it as an illegal call.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			toolCallResponse(makeToolCall("missing", nil)),
			toolCallResponse(makeToolCall("missing", nil)),
			textResponse("forced after executor unavailable"),
		},
	}
	cfg := baseCfg(mock, &mockExecutor{results: map[string]string{}})
	cfg.Executor = &DirectExecutor{
		Exec: func(_ context.Context, name, _ string) (string, error) {
			return "", &tools.ErrToolUnavailable{ToolName: name}
		},
	}

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Exhausted {
		t.Error("should be exhausted due to illegal tool from executor")
	}
	if result.ExhaustReason != ExhaustIllegalTool {
		t.Errorf("exhaust reason = %q", result.ExhaustReason)
	}
}

func TestEngine_IterationRecords(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			toolCallResponse(makeToolCall("a", nil)),
			textResponse("done"),
		},
	}
	exec := &mockExecutor{results: map[string]string{"a": "ok"}}
	cfg := baseCfg(mock, exec)

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Iterations) != 2 {
		t.Fatalf("iterations = %d, want 2", len(result.Iterations))
	}
	if !result.Iterations[0].HasToolCalls {
		t.Error("iteration 0 should have tool calls")
	}
	if result.Iterations[1].HasToolCalls {
		t.Error("iteration 1 should not have tool calls")
	}
	if result.Iterations[0].ToolCallIDs[0] != "tc_a" {
		t.Errorf("tool call ID = %q, want %q", result.Iterations[0].ToolCallIDs[0], "tc_a")
	}
}

func TestEngine_ForceTextOnExhaustionWithNoPendingTools(t *testing.T) {
	// When loop exhausts but last message is NOT a tool result,
	// forceText should not make an extra call.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			textResponse(""), // Empty on first iteration.
			textResponse(""), // Empty after nudge.
		},
	}
	exec := &mockExecutor{results: map[string]string{}}
	cfg := baseCfg(mock, exec)
	cfg.NudgeOnEmpty = false
	cfg.MaxIterations = 1

	engine := &Engine{}
	result, err := engine.Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With NudgeOnEmpty disabled, an empty first-turn response is still
	// preserved as empty so callers can decide how to handle it.
	if result.Exhausted {
		t.Error("single text response should not be exhausted")
	}
}

func TestEngine_ToolDefsNames(t *testing.T) {
	defs := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "search"}},
		{"type": "function", "function": map[string]any{"name": "read_file"}},
	}
	names := toolDefsNames(defs)
	if len(names) != 2 || names[0] != "search" || names[1] != "read_file" {
		t.Errorf("names = %v, want [search read_file]", names)
	}
}

func TestEngine_ToolDefsNamesEmpty(t *testing.T) {
	names := toolDefsNames(nil)
	if names != nil {
		t.Errorf("names = %v, want nil", names)
	}
}
