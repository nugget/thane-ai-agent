package delegate

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// mockLLMClient returns pre-configured responses in sequence.
type mockLLMClient struct {
	mu        sync.Mutex
	responses []*llm.ChatResponse
	callIndex int
	calls     []mockCall
}

type mockCall struct {
	Model    string
	Messages []llm.Message
	Tools    []map[string]any
}

func (m *mockLLMClient) Chat(ctx context.Context, model string, messages []llm.Message, toolDefs []map[string]any) (*llm.ChatResponse, error) {
	return m.ChatStream(ctx, model, messages, toolDefs, nil)
}

func (m *mockLLMClient) ChatStream(_ context.Context, model string, messages []llm.Message, toolDefs []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls = append(m.calls, mockCall{Model: model, Messages: messages, Tools: toolDefs})

	if m.callIndex >= len(m.responses) {
		return nil, fmt.Errorf("mock: no more responses (call %d)", m.callIndex)
	}

	resp := m.responses[m.callIndex]
	m.callIndex++
	return resp, nil
}

func (m *mockLLMClient) Ping(_ context.Context) error { return nil }

func newTestRegistry() *tools.Registry {
	r := tools.NewEmptyRegistry()
	r.Register(&tools.Tool{
		Name:        "get_state",
		Description: "Get entity state",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			entityID, _ := args["entity_id"].(string)
			return fmt.Sprintf("Entity: %s\nState: on", entityID), nil
		},
	})
	r.Register(&tools.Tool{
		Name:        "web_search",
		Description: "Search the web",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "search results", nil
		},
	})
	r.Register(&tools.Tool{
		Name:        "thane_delegate",
		Description: "Should be excluded",
		Parameters:  map[string]any{},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "this should never be called by a delegate", nil
		},
	})
	return r
}

func TestExecute_SimpleTextResponse(t *testing.T) {
	mock := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "The light is on."},
				InputTokens:  100,
				OutputTokens: 20,
			},
		},
	}

	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	result, err := exec.Execute(context.Background(), "Check the office light", "general", "")

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Content != "The light is on." {
		t.Errorf("Content = %q, want %q", result.Content, "The light is on.")
	}
	if result.Model != "test-model" {
		t.Errorf("Model = %q, want %q", result.Model, "test-model")
	}
	if result.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1", result.Iterations)
	}
	if result.Exhausted {
		t.Error("Exhausted = true, want false")
	}
}

func TestExecute_WithToolCalls(t *testing.T) {
	mock := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{
				Model: "test-model",
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{
							ID: "call-1",
							Function: struct {
								Name      string         `json:"name"`
								Arguments map[string]any `json:"arguments"`
							}{
								Name:      "get_state",
								Arguments: map[string]any{"entity_id": "light.office"},
							},
						},
					},
				},
				InputTokens:  100,
				OutputTokens: 30,
			},
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "The office light is on."},
				InputTokens:  200,
				OutputTokens: 25,
			},
		},
	}

	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	result, err := exec.Execute(context.Background(), "Check the office light", "ha", "")

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Content != "The office light is on." {
		t.Errorf("Content = %q, want %q", result.Content, "The office light is on.")
	}
	if result.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", result.Iterations)
	}
	if result.InputTokens != 300 {
		t.Errorf("InputTokens = %d, want 300", result.InputTokens)
	}
	if result.OutputTokens != 55 {
		t.Errorf("OutputTokens = %d, want 55", result.OutputTokens)
	}
}

func TestExecute_MaxIterationsExhausted(t *testing.T) {
	// Always return tool calls to exhaust the iteration budget.
	toolCallResp := &llm.ChatResponse{
		Model: "test-model",
		Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{
					ID: "call-loop",
					Function: struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					}{
						Name:      "get_state",
						Arguments: map[string]any{"entity_id": "light.test"},
					},
				},
			},
		},
		InputTokens:  50,
		OutputTokens: 20,
	}

	// Build exactly maxIter tool call responses + 1 forced text response.
	var responses []*llm.ChatResponse
	for range defaultMaxIter {
		responses = append(responses, toolCallResp)
	}
	// The forced text response (tools=nil call after budget exhaustion).
	responses = append(responses, &llm.ChatResponse{
		Model:        "test-model",
		Message:      llm.Message{Role: "assistant", Content: "Partial results here."},
		InputTokens:  100,
		OutputTokens: 30,
	})

	mock := &mockLLMClient{responses: responses}
	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	result, err := exec.Execute(context.Background(), "Do something complex", "general", "")

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Exhausted {
		t.Error("Exhausted = false, want true")
	}
	if result.Content != "Partial results here." {
		t.Errorf("Content = %q, want %q", result.Content, "Partial results here.")
	}
}

func TestExecute_TokenBudgetExhausted(t *testing.T) {
	// Return a tool call response with high output tokens to blow the budget.
	toolCallResp := &llm.ChatResponse{
		Model: "test-model",
		Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{
					ID: "call-1",
					Function: struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					}{
						Name:      "get_state",
						Arguments: map[string]any{"entity_id": "light.test"},
					},
				},
			},
		},
		InputTokens:  100,
		OutputTokens: 60000, // Exceeds default 50K budget
	}

	forcedText := &llm.ChatResponse{
		Model:        "test-model",
		Message:      llm.Message{Role: "assistant", Content: "Budget exceeded partial results."},
		InputTokens:  200,
		OutputTokens: 30,
	}

	mock := &mockLLMClient{responses: []*llm.ChatResponse{toolCallResp, forcedText}}
	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	result, err := exec.Execute(context.Background(), "Expensive task", "general", "")

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Exhausted {
		t.Error("Exhausted = false, want true")
	}
}

func TestExecute_EmptyTask(t *testing.T) {
	exec := NewExecutor(slog.Default(), &mockLLMClient{}, nil, newTestRegistry(), "test-model")
	_, err := exec.Execute(context.Background(), "", "general", "")

	if err == nil {
		t.Fatal("Execute() with empty task should return error")
	}
}

func TestExecute_UnknownProfileDefaultsToGeneral(t *testing.T) {
	mock := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Done."},
				InputTokens:  50,
				OutputTokens: 10,
			},
		},
	}

	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	result, err := exec.Execute(context.Background(), "Do something", "nonexistent_profile", "")

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Content != "Done." {
		t.Errorf("Content = %q, want %q", result.Content, "Done.")
	}
}

func TestExecute_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	exec := NewExecutor(slog.Default(), &mockLLMClient{}, nil, newTestRegistry(), "test-model")
	_, err := exec.Execute(ctx, "Do something", "general", "")

	if err == nil {
		t.Fatal("Execute() with cancelled context should return error")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("error = %q, want to contain 'cancelled'", err.Error())
	}
}

func TestExecute_HAProfileExcludesNonHATools(t *testing.T) {
	// The HA profile should not have web_search or thane_delegate.
	mock := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{
				Model: "test-model",
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{
							ID: "call-1",
							Function: struct {
								Name      string         `json:"name"`
								Arguments map[string]any `json:"arguments"`
							}{
								Name:      "web_search",
								Arguments: map[string]any{"query": "test"},
							},
						},
					},
				},
				InputTokens:  100,
				OutputTokens: 20,
			},
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Failed to search."},
				InputTokens:  100,
				OutputTokens: 10,
			},
		},
	}

	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	result, err := exec.Execute(context.Background(), "Search the web", "ha", "")

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	// The tool call should fail because web_search is not in the HA profile.
	// The delegate should still complete with the error in the tool result.
	if result.Content != "Failed to search." {
		t.Errorf("Content = %q, want %q", result.Content, "Failed to search.")
	}
}

func TestToolHandler_ValidArgs(t *testing.T) {
	mock := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Task done."},
				InputTokens:  50,
				OutputTokens: 10,
			},
		},
	}

	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	handler := ToolHandler(exec)

	result, err := handler(context.Background(), map[string]any{
		"task":     "Check the lights",
		"profile":  "ha",
		"guidance": "Focus on the office",
	})

	if err != nil {
		t.Fatalf("ToolHandler() error = %v", err)
	}
	if !strings.Contains(result, "[Delegate SUCCEEDED:") {
		t.Errorf("result = %q, want to contain '[Delegate SUCCEEDED:'", result)
	}
	if !strings.Contains(result, "Task done.") {
		t.Errorf("result = %q, want to contain 'Task done.'", result)
	}
}

func TestToolHandler_EmptyTask(t *testing.T) {
	exec := NewExecutor(slog.Default(), &mockLLMClient{}, nil, newTestRegistry(), "test-model")
	handler := ToolHandler(exec)

	result, err := handler(context.Background(), map[string]any{})

	if err != nil {
		t.Fatalf("ToolHandler() error = %v, want nil", err)
	}
	if !strings.Contains(result, "Error: task is required") {
		t.Errorf("result = %q, want to contain 'task is required'", result)
	}
}

func TestToolHandler_DefaultProfile(t *testing.T) {
	mock := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Done."},
				InputTokens:  50,
				OutputTokens: 10,
			},
		},
	}

	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	handler := ToolHandler(exec)

	result, err := handler(context.Background(), map[string]any{
		"task": "Do something",
	})

	if err != nil {
		t.Fatalf("ToolHandler() error = %v", err)
	}
	if !strings.Contains(result, "profile=general") {
		t.Errorf("result = %q, want to contain 'profile=general'", result)
	}
}

func TestBuiltinProfiles_GeneralForcesLocalOnly(t *testing.T) {
	profiles := builtinProfiles()
	general, ok := profiles["general"]
	if !ok {
		t.Fatal("missing 'general' profile")
	}

	if general.RouterHints == nil {
		t.Fatal("general profile RouterHints is nil, want HintLocalOnly=true")
	}
	if general.RouterHints[router.HintLocalOnly] != "true" {
		t.Errorf("general profile HintLocalOnly = %q, want %q",
			general.RouterHints[router.HintLocalOnly], "true")
	}
}

func TestBuiltinProfiles_HAForcesLocalOnly(t *testing.T) {
	profiles := builtinProfiles()
	ha, ok := profiles["ha"]
	if !ok {
		t.Fatal("missing 'ha' profile")
	}

	if ha.RouterHints[router.HintLocalOnly] != "true" {
		t.Errorf("ha profile HintLocalOnly = %q, want %q",
			ha.RouterHints[router.HintLocalOnly], "true")
	}
	if ha.RouterHints[router.HintMission] != "device_control" {
		t.Errorf("ha profile HintMission = %q, want %q",
			ha.RouterHints[router.HintMission], "device_control")
	}
}

func TestExecute_GeneralProfileSelectsLocalModel(t *testing.T) {
	// Create a router with a cheap local model and an expensive cloud model.
	rtr := router.NewRouter(slog.Default(), router.Config{
		DefaultModel: "local-model",
		LocalFirst:   true,
		Models: []router.Model{
			{Name: "local-model", Provider: "ollama", SupportsTools: true, Speed: 8, Quality: 5, CostTier: 0, ContextWindow: 8192},
			{Name: "cloud-model", Provider: "anthropic", SupportsTools: true, Speed: 6, Quality: 10, CostTier: 3, ContextWindow: 8192},
		},
		MaxAuditLog: 10,
	})

	mock := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{
				Model:        "local-model",
				Message:      llm.Message{Role: "assistant", Content: "Found the archives."},
				InputTokens:  100,
				OutputTokens: 20,
			},
		},
	}

	exec := NewExecutor(slog.Default(), mock, rtr, newTestRegistry(), "local-model")
	result, err := exec.Execute(context.Background(), "search IRC archives for distributed.net history", "general", "")

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Model == "cloud-model" {
		t.Errorf("Model = %q, want local model (general profile should force local-only)", result.Model)
	}
	if result.Model != "local-model" {
		t.Errorf("Model = %q, want %q", result.Model, "local-model")
	}
}

func TestToolHandler_ExhaustedOutput(t *testing.T) {
	// Always return tool calls to exhaust the iteration budget.
	toolCallResp := &llm.ChatResponse{
		Model: "test-model",
		Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{
					ID: "call-loop",
					Function: struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					}{
						Name:      "get_state",
						Arguments: map[string]any{"entity_id": "light.test"},
					},
				},
			},
		},
		InputTokens:  500,
		OutputTokens: 200,
	}

	var responses []*llm.ChatResponse
	for range defaultMaxIter {
		responses = append(responses, toolCallResp)
	}
	// Forced text response after budget exhaustion.
	responses = append(responses, &llm.ChatResponse{
		Model:        "test-model",
		Message:      llm.Message{Role: "assistant", Content: "Partial results from exhausted delegate."},
		InputTokens:  100,
		OutputTokens: 30,
	})

	mock := &mockLLMClient{responses: responses}
	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	handler := ToolHandler(exec)

	result, err := handler(context.Background(), map[string]any{
		"task":    "Do something complex",
		"profile": "general",
	})

	if err != nil {
		t.Fatalf("ToolHandler() error = %v", err)
	}
	if !strings.Contains(result, "[Delegate FAILED:") {
		t.Errorf("result missing failure header, got: %s", result)
	}
	if !strings.Contains(result, "tokens_in=") {
		t.Errorf("result missing tokens_in, got: %s", result)
	}
	if !strings.Contains(result, "tokens_out=") {
		t.Errorf("result missing tokens_out, got: %s", result)
	}
	if !strings.Contains(result, "Partial results from exhausted delegate.") {
		t.Errorf("result missing delegate content, got: %s", result)
	}
	if !strings.Contains(result, "[Exhaustion note:") {
		t.Errorf("result missing exhaustion note, got: %s", result)
	}
	if !strings.Contains(result, "more specific guidance") {
		t.Errorf("result missing retry guidance, got: %s", result)
	}
	if !strings.Contains(result, "reason=max_iterations") {
		t.Errorf("result missing exhaust reason, got: %s", result)
	}
	if !strings.Contains(result, "used all available iterations") {
		t.Errorf("result missing reason-specific text for max_iterations, got: %s", result)
	}
}

func TestExecute_WallClockExhausted(t *testing.T) {
	// Slow mock: each LLM call sleeps briefly so wall clock exceeds a tiny MaxDuration.
	slowMock := &slowLLMClient{
		delay: 20 * time.Millisecond,
		resp: &llm.ChatResponse{
			Model: "test-model",
			Message: llm.Message{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{
					{
						ID: "call-slow",
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{
							Name:      "get_state",
							Arguments: map[string]any{"entity_id": "light.test"},
						},
					},
				},
			},
			InputTokens:  100,
			OutputTokens: 20,
		},
		forcedTextResp: &llm.ChatResponse{
			Model:        "test-model",
			Message:      llm.Message{Role: "assistant", Content: "Ran out of time."},
			InputTokens:  50,
			OutputTokens: 10,
		},
	}

	exec := NewExecutor(slog.Default(), slowMock, nil, newTestRegistry(), "test-model")
	// Override the general profile with a tiny MaxDuration.
	exec.profiles["general"].MaxDuration = 30 * time.Millisecond

	result, err := exec.Execute(context.Background(), "Slow task", "general", "")

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Exhausted {
		t.Error("Exhausted = false, want true")
	}
	if result.ExhaustReason != ExhaustWallClock {
		t.Errorf("ExhaustReason = %q, want %q", result.ExhaustReason, ExhaustWallClock)
	}
}

// slowLLMClient adds a delay to each call and returns tool calls until
// tools=nil is passed (forced text response).
type slowLLMClient struct {
	delay          time.Duration
	resp           *llm.ChatResponse
	forcedTextResp *llm.ChatResponse
	mu             sync.Mutex
	calls          int
}

func (m *slowLLMClient) Chat(ctx context.Context, model string, messages []llm.Message, toolDefs []map[string]any) (*llm.ChatResponse, error) {
	return m.ChatStream(ctx, model, messages, toolDefs, nil)
}

func (m *slowLLMClient) ChatStream(ctx context.Context, _ string, _ []llm.Message, toolDefs []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()

	// Respect context cancellation during the delay.
	select {
	case <-time.After(m.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// tools=nil means forced text response.
	if toolDefs == nil {
		return m.forcedTextResp, nil
	}
	return m.resp, nil
}

func (m *slowLLMClient) Ping(_ context.Context) error { return nil }

// blockingLLMClient blocks on ChatStream until the context is cancelled,
// simulating a hanging LLM provider (e.g. Ollama dropping a connection).
type blockingLLMClient struct {
	mu    sync.Mutex
	calls int
}

func (m *blockingLLMClient) Chat(ctx context.Context, model string, messages []llm.Message, toolDefs []map[string]any) (*llm.ChatResponse, error) {
	return m.ChatStream(ctx, model, messages, toolDefs, nil)
}

func (m *blockingLLMClient) ChatStream(ctx context.Context, _ string, _ []llm.Message, _ []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()

	// Block until context is cancelled (simulating a hanging LLM).
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *blockingLLMClient) Ping(_ context.Context) error { return nil }

func TestExecute_ContextDeadlineCancelsBlockingLLM(t *testing.T) {
	mock := &blockingLLMClient{}

	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	exec.profiles["general"].MaxDuration = 100 * time.Millisecond

	start := time.Now()
	result, err := exec.Execute(context.Background(), "Blocking task", "general", "")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute() returned error: %v (should return exhausted result)", err)
	}
	if !result.Exhausted {
		t.Error("Exhausted = false, want true")
	}
	if result.ExhaustReason != ExhaustWallClock {
		t.Errorf("ExhaustReason = %q, want %q", result.ExhaustReason, ExhaustWallClock)
	}
	// The key assertion: execution completed within a reasonable multiple
	// of MaxDuration, not the 5-minute HTTP timeout or indefinitely.
	if elapsed > 2*time.Second {
		t.Errorf("Execute took %v, want < 2s (context deadline should have fired at ~100ms)", elapsed)
	}
}

func TestExecute_ExhaustReasonMaxIterations(t *testing.T) {
	toolCallResp := &llm.ChatResponse{
		Model: "test-model",
		Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{
					ID: "call-loop",
					Function: struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					}{
						Name:      "get_state",
						Arguments: map[string]any{"entity_id": "light.test"},
					},
				},
			},
		},
		InputTokens:  50,
		OutputTokens: 20,
	}

	var responses []*llm.ChatResponse
	for range defaultMaxIter {
		responses = append(responses, toolCallResp)
	}
	responses = append(responses, &llm.ChatResponse{
		Model:        "test-model",
		Message:      llm.Message{Role: "assistant", Content: "Done."},
		InputTokens:  100,
		OutputTokens: 30,
	})

	mock := &mockLLMClient{responses: responses}
	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	result, err := exec.Execute(context.Background(), "Loop task", "general", "")

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Exhausted {
		t.Error("Exhausted = false, want true")
	}
	if result.ExhaustReason != ExhaustMaxIterations {
		t.Errorf("ExhaustReason = %q, want %q", result.ExhaustReason, ExhaustMaxIterations)
	}
}

func TestExecute_ExhaustReasonTokenBudget(t *testing.T) {
	toolCallResp := &llm.ChatResponse{
		Model: "test-model",
		Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{
					ID: "call-1",
					Function: struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					}{
						Name:      "get_state",
						Arguments: map[string]any{"entity_id": "light.test"},
					},
				},
			},
		},
		InputTokens:  100,
		OutputTokens: 60000, // Exceeds default 25K budget
	}

	forcedText := &llm.ChatResponse{
		Model:        "test-model",
		Message:      llm.Message{Role: "assistant", Content: "Budget blown."},
		InputTokens:  200,
		OutputTokens: 30,
	}

	mock := &mockLLMClient{responses: []*llm.ChatResponse{toolCallResp, forcedText}}
	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	result, err := exec.Execute(context.Background(), "Expensive task", "general", "")

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Exhausted {
		t.Error("Exhausted = false, want true")
	}
	if result.ExhaustReason != ExhaustTokenBudget {
		t.Errorf("ExhaustReason = %q, want %q", result.ExhaustReason, ExhaustTokenBudget)
	}
}

func TestDefaultBudgets(t *testing.T) {
	if defaultMaxIter != 15 {
		t.Errorf("defaultMaxIter = %d, want 15", defaultMaxIter)
	}
	if defaultMaxTokens != 25000 {
		t.Errorf("defaultMaxTokens = %d, want 25000", defaultMaxTokens)
	}
	if defaultMaxDuration != 90*time.Second {
		t.Errorf("defaultMaxDuration = %v, want 90s", defaultMaxDuration)
	}
}

func TestExecute_EmptyResultAfterToolCalls(t *testing.T) {
	// Issue #162: When a delegate runs tool calls but the final response
	// is empty, it should be flagged as exhausted with reason "no_output".
	mock := &mockLLMClient{
		responses: []*llm.ChatResponse{
			// Iter 0: tool call
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
							Name:      "get_state",
							Arguments: map[string]any{"entity_id": "light.office"},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 30,
			},
			// Iter 1: empty content, no tool calls
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: ""},
				InputTokens:  200,
				OutputTokens: 2,
			},
		},
	}

	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	result, err := exec.Execute(context.Background(), "Check the office light", "general", "")

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Exhausted {
		t.Error("Exhausted = false, want true")
	}
	if result.ExhaustReason != ExhaustNoOutput {
		t.Errorf("ExhaustReason = %q, want %q", result.ExhaustReason, ExhaustNoOutput)
	}
	if result.Content != "" {
		t.Errorf("Content = %q, want empty", result.Content)
	}
	if result.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", result.Iterations)
	}
}

func TestExecute_EmptyResultFirstIter(t *testing.T) {
	// Empty result on the first iteration (no prior tool calls) should NOT
	// be flagged as no_output — it's a different condition.
	mock := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: ""},
				InputTokens:  100,
				OutputTokens: 2,
			},
		},
	}

	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	result, err := exec.Execute(context.Background(), "Do something", "general", "")

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Exhausted {
		t.Error("Exhausted = true, want false (first-iteration empty should not be flagged)")
	}
	if result.ExhaustReason != "" {
		t.Errorf("ExhaustReason = %q, want empty", result.ExhaustReason)
	}
}

func TestToolHandler_SucceededHeader(t *testing.T) {
	mock := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Task done."},
				InputTokens:  50,
				OutputTokens: 10,
			},
		},
	}

	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	handler := ToolHandler(exec)

	result, err := handler(context.Background(), map[string]any{"task": "Check the lights"})
	if err != nil {
		t.Fatalf("ToolHandler() error = %v", err)
	}
	if !strings.Contains(result, "[Delegate SUCCEEDED:") {
		t.Errorf("result = %q, want to contain '[Delegate SUCCEEDED:'", result)
	}
	if strings.Contains(result, "FAILED") {
		t.Errorf("successful result should not contain 'FAILED', got: %s", result)
	}
}

func TestToolHandler_FailedHeaderNoOutput(t *testing.T) {
	// After tool calls, empty content → ExhaustNoOutput → FAILED header.
	mock := &mockLLMClient{
		responses: []*llm.ChatResponse{
			// Iter 0: tool call
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
							Name:      "get_state",
							Arguments: map[string]any{"entity_id": "light.test"},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 30,
			},
			// Iter 1: empty content
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: ""},
				InputTokens:  200,
				OutputTokens: 2,
			},
		},
	}

	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	handler := ToolHandler(exec)

	result, err := handler(context.Background(), map[string]any{"task": "Check lights"})
	if err != nil {
		t.Fatalf("ToolHandler() error = %v", err)
	}
	if !strings.Contains(result, "[Delegate FAILED:") {
		t.Errorf("result = %q, want to contain '[Delegate FAILED:'", result)
	}
	if !strings.Contains(result, "reason=no_output") {
		t.Errorf("result = %q, want to contain 'reason=no_output'", result)
	}
	if !strings.Contains(result, "produced no text output") {
		t.Errorf("result missing no_output exhaustion note, got: %s", result)
	}
	if strings.Contains(result, "SUCCEEDED") {
		t.Errorf("failed result should not contain 'SUCCEEDED', got: %s", result)
	}
}
