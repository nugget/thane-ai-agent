package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// mockLLM returns pre-configured responses in sequence and records each call.
type mockLLM struct {
	mu        sync.Mutex
	responses []*llm.ChatResponse
	callIndex int
	calls     []mockLLMCall
}

type mockLLMCall struct {
	Model    string
	Messages []llm.Message
	Tools    []map[string]any
}

func (m *mockLLM) Chat(_ context.Context, model string, msgs []llm.Message, td []map[string]any) (*llm.ChatResponse, error) {
	return m.ChatStream(context.Background(), model, msgs, td, nil)
}

func (m *mockLLM) ChatStream(_ context.Context, model string, msgs []llm.Message, td []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls = append(m.calls, mockLLMCall{Model: model, Messages: msgs, Tools: td})

	if m.callIndex >= len(m.responses) {
		return nil, fmt.Errorf("mockLLM: no more responses (call %d)", m.callIndex)
	}
	resp := m.responses[m.callIndex]
	m.callIndex++
	return resp, nil
}

func (m *mockLLM) Ping(_ context.Context) error { return nil }

// mockMem is a minimal in-memory MemoryStore for tests.
type mockMem struct {
	msgs map[string][]memory.Message
}

func newMockMem() *mockMem { return &mockMem{msgs: make(map[string][]memory.Message)} }

func (m *mockMem) GetMessages(id string) []memory.Message { return m.msgs[id] }
func (m *mockMem) AddMessage(id, role, content string) error {
	m.msgs[id] = append(m.msgs[id], memory.Message{Role: role, Content: content})
	return nil
}
func (m *mockMem) GetTokenCount(string) int { return 0 }
func (m *mockMem) Clear(id string) error    { m.msgs[id] = nil; return nil }
func (m *mockMem) Stats() map[string]any    { return nil }

// toolNames extracts the function names from a tool definitions slice.
func toolNames(defs []map[string]any) []string {
	var names []string
	for _, d := range defs {
		fn, ok := d["function"].(map[string]any)
		if !ok {
			continue
		}
		if name, ok := fn["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

func hasName(names []string, target string) bool {
	for _, n := range names {
		if n == target {
			return true
		}
	}
	return false
}

// buildTestLoop creates a Loop with a mock LLM and a registry containing
// built-in tools plus the given additional tool names. Tools are no-ops;
// only their names matter for gating tests.
func buildTestLoop(mock *mockLLM, extraNames []string) *Loop {
	reg := tools.NewRegistry(nil, nil)
	for _, name := range extraNames {
		n := name // capture
		reg.Register(&tools.Tool{
			Name:        n,
			Description: "test tool " + n,
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				return "ok", nil
			},
		})
	}

	l := &Loop{
		logger: slog.Default(),
		memory: newMockMem(),
		llm:    mock,
		tools:  reg,
		model:  "test-model",
	}
	return l
}

func TestToolGating_RestrictedAllIterations(t *testing.T) {
	// With gating active, ALL iterations should see only the restricted
	// orchestrator tool set. The mock returns a tool call on the first
	// iteration (triggering a second) and a text response on the second
	// so we can inspect both calls.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// First iteration: model calls thane_now
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
							Name:      "thane_now",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 10,
			},
			// Second iteration: text response
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Done."},
				InputTokens:  200,
				OutputTokens: 5,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"thane_now", "recall_fact", "web_search"})
	loop.SetOrchestratorTools([]string{"thane_now", "recall_fact"})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "check the lights"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(mock.calls) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(mock.calls))
	}

	// Both iterations should only have the restricted tool set.
	for _, idx := range []int{0, 1} {
		names := toolNames(mock.calls[idx].Tools)
		if len(names) != 2 {
			t.Errorf("call[%d] tool count = %d, want 2; tools: %v", idx, len(names), names)
		}
		if !hasName(names, "thane_now") {
			t.Errorf("call[%d] tools missing thane_now: %v", idx, names)
		}
		if !hasName(names, "recall_fact") {
			t.Errorf("call[%d] tools missing recall_fact: %v", idx, names)
		}
		if hasName(names, "web_search") {
			t.Errorf("call[%d] tools should NOT contain web_search: %v", idx, names)
		}
	}
}

func TestToolExecutionContext_PropagatesRequestHints(t *testing.T) {
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
							Name:      "inspect_hints",
							Arguments: map[string]any{},
						},
					}},
				},
			},
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Done."},
				InputTokens:  20,
				OutputTokens: 5,
			},
		},
	}

	loop := buildTestLoop(mock, nil)

	var captured map[string]string
	loop.tools.Register(&tools.Tool{
		Name:        "inspect_hints",
		Description: "inspect hints",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			hints := tools.HintsFromContext(ctx)
			captured = make(map[string]string, len(hints))
			for k, v := range hints {
				captured[k] = v
			}
			return "ok", nil
		},
	})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "inspect"}},
		Hints: map[string]string{
			"source": "owu",
			router.DelegateHintKey(router.HintQualityFloor): "10",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if captured["source"] != "owu" {
		t.Fatalf("captured source = %q, want owu", captured["source"])
	}
	if captured[router.DelegateHintKey(router.HintQualityFloor)] != "10" {
		t.Fatalf("captured delegate quality floor = %q, want 10", captured[router.DelegateHintKey(router.HintQualityFloor)])
	}
}

func TestToolGating_RestrictedAcrossMultipleToolCalls(t *testing.T) {
	// Verify that gating persists across multiple iterations with tool
	// calls — the model never sees the full tool set.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// First iteration: delegate call
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
							Name:      "thane_now",
							Arguments: map[string]any{},
						},
					}},
				},
			},
			// Second iteration: another delegate call (re-delegation)
			{
				Model: "test-model",
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID: "call-2",
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{
							Name:      "thane_now",
							Arguments: map[string]any{},
						},
					}},
				},
			},
			// Third iteration: text response
			{
				Model:   "test-model",
				Message: llm.Message{Role: "assistant", Content: "Done."},
			},
		},
	}

	loop := buildTestLoop(mock, []string{"thane_now", "recall_fact", "get_state", "web_search"})
	loop.SetOrchestratorTools([]string{"thane_now"})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "test"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(mock.calls) < 3 {
		t.Fatalf("expected 3 LLM calls, got %d", len(mock.calls))
	}

	// ALL iterations should have only the restricted set.
	for _, idx := range []int{0, 1, 2} {
		names := toolNames(mock.calls[idx].Tools)
		if len(names) != 1 {
			t.Errorf("call[%d] tool count = %d, want 1; tools: %v", idx, len(names), names)
		}
		if !hasName(names, "thane_now") {
			t.Errorf("call[%d] tools missing thane_now: %v", idx, names)
		}
		if hasName(names, "get_state") {
			t.Errorf("call[%d] tools should NOT contain get_state: %v", idx, names)
		}
	}
}

func TestOrchestratorToolGating_DisabledWhenNoDelegation(t *testing.T) {
	// When orchestratorTools is set but thane_now is NOT in the
	// registry, gating should be auto-disabled — all tools visible.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:   "test-model",
				Message: llm.Message{Role: "assistant", Content: "No delegation available."},
			},
		},
	}

	// Note: thane_now is NOT in the registry (not in extraNames).
	loop := buildTestLoop(mock, []string{"recall_fact"})
	fullToolCount := len(loop.tools.List())
	loop.SetOrchestratorTools([]string{"thane_now", "recall_fact"})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "test"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(mock.calls))
	}

	// All tools should be visible because gating is disabled (no thane_now).
	names := toolNames(mock.calls[0].Tools)
	if len(names) != fullToolCount {
		t.Errorf("tool count = %d, want %d (gating should be disabled); tools: %v", len(names), fullToolCount, names)
	}
}

func TestOrchestratorToolGating_DisabledWhenEmpty(t *testing.T) {
	// When orchestratorTools is nil/empty, all tools should be available.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:   "test-model",
				Message: llm.Message{Role: "assistant", Content: "All tools available."},
			},
		},
	}

	loop := buildTestLoop(mock, []string{"thane_now", "recall_fact"})
	fullToolCount := len(loop.tools.List())
	// Don't call SetOrchestratorTools — leave nil.

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "test"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	names := toolNames(mock.calls[0].Tools)
	if len(names) != fullToolCount {
		t.Errorf("tool count = %d, want %d; tools: %v", len(names), fullToolCount, names)
	}
}

func TestToolGating_DisabledByDelegationGatingHint(t *testing.T) {
	// When the delegation_gating hint is "disabled" (thane:ops profile),
	// gating should be bypassed even when orchestratorTools and
	// thane_now are both present.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:   "test-model",
				Message: llm.Message{Role: "assistant", Content: "Direct tool access."},
			},
		},
	}

	loop := buildTestLoop(mock, []string{"thane_now", "recall_fact", "web_search"})
	fullToolCount := len(loop.tools.List())
	loop.SetOrchestratorTools([]string{"thane_now", "recall_fact"})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "deploy the update"}},
		Hints:    map[string]string{"delegation_gating": "disabled"},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(mock.calls))
	}

	// All tools should be visible because gating is disabled via hint.
	names := toolNames(mock.calls[0].Tools)
	if len(names) != fullToolCount {
		t.Errorf("tool count = %d, want %d (gating should be disabled by hint); tools: %v", len(names), fullToolCount, names)
	}
}

func TestToolGating_BlocksCallsOutsideOrchestratorSet(t *testing.T) {
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
							Name:      "web_search",
							Arguments: map[string]any{"q": "locks"},
						},
					}},
				},
			},
			{
				Model:   "test-model",
				Message: llm.Message{Role: "assistant", Content: "Recovered."},
			},
		},
	}

	loop := buildTestLoop(mock, []string{"thane_now", "web_search"})
	loop.SetOrchestratorTools([]string{"thane_now"})

	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "delegate the work"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if resp.Content != "Recovered." {
		t.Fatalf("Content = %q, want %q", resp.Content, "Recovered.")
	}
	if len(mock.calls) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(mock.calls))
	}

	names := toolNames(mock.calls[0].Tools)
	if len(names) != 1 || !hasName(names, "thane_now") {
		t.Fatalf("first call tools = %v, want only thane_now", names)
	}

	msgs := mock.calls[1].Messages
	if len(msgs) == 0 {
		t.Fatal("second call messages = empty, want tool recovery context")
	}
	last := msgs[len(msgs)-1]
	if last.Role != "tool" {
		t.Fatalf("last message role = %q, want tool", last.Role)
	}
	want := fmt.Sprintf(prompts.IllegalToolMessage, "web_search")
	if last.Content != want {
		t.Fatalf("tool recovery content = %q, want %q", last.Content, want)
	}
}
