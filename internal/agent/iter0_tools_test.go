package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/memory"
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
		logger:  slog.Default(),
		memory:  newMockMem(),
		llm:     mock,
		tools:   reg,
		model:   "test-model",
		talents: "",
	}
	return l
}

func TestIter0ToolGating_RestrictedFirstIteration(t *testing.T) {
	// On iter-0 with gating active, only the restricted tools should be
	// visible. The mock returns a tool call on iter-0 (triggering iter-1)
	// and a text response on iter-1 so we can inspect both calls.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// Iter-0: model calls thane_delegate
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
							Name:      "thane_delegate",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 10,
			},
			// Iter-1: text response
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Done."},
				InputTokens:  200,
				OutputTokens: 5,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"thane_delegate", "recall_fact", "web_search"})
	fullToolCount := len(loop.tools.List())
	loop.SetIter0Tools([]string{"thane_delegate", "recall_fact"})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "check the lights"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(mock.calls) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(mock.calls))
	}

	// Iter-0 should only have the restricted tool set.
	iter0Names := toolNames(mock.calls[0].Tools)
	if len(iter0Names) != 2 {
		t.Errorf("iter-0 tool count = %d, want 2; tools: %v", len(iter0Names), iter0Names)
	}
	if !hasName(iter0Names, "thane_delegate") {
		t.Errorf("iter-0 tools missing thane_delegate: %v", iter0Names)
	}
	if !hasName(iter0Names, "recall_fact") {
		t.Errorf("iter-0 tools missing recall_fact: %v", iter0Names)
	}
	if hasName(iter0Names, "get_state") {
		t.Errorf("iter-0 tools should NOT contain get_state: %v", iter0Names)
	}
	if hasName(iter0Names, "web_search") {
		t.Errorf("iter-0 tools should NOT contain web_search: %v", iter0Names)
	}

	// Iter-1 should have the full tool set.
	iter1Names := toolNames(mock.calls[1].Tools)
	if len(iter1Names) != fullToolCount {
		t.Errorf("iter-1 tool count = %d, want %d; tools: %v", len(iter1Names), fullToolCount, iter1Names)
	}
}

func TestIter0ToolGating_FullToolsAfterFirstIteration(t *testing.T) {
	// Verify iter-1+ always gets the full tool set even with gating enabled.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// Iter-0: delegate call
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
							Name:      "thane_delegate",
							Arguments: map[string]any{},
						},
					}},
				},
			},
			// Iter-1: another tool call (now using full set)
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
							Name:      "get_state",
							Arguments: map[string]any{},
						},
					}},
				},
			},
			// Iter-2: text response
			{
				Model:   "test-model",
				Message: llm.Message{Role: "assistant", Content: "Done."},
			},
		},
	}

	loop := buildTestLoop(mock, []string{"thane_delegate", "recall_fact", "get_state", "web_search"})
	fullToolCount := len(loop.tools.List())
	loop.SetIter0Tools([]string{"thane_delegate"})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "test"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(mock.calls) < 3 {
		t.Fatalf("expected 3 LLM calls, got %d", len(mock.calls))
	}

	// Both iter-1 and iter-2 should have full tools.
	for _, idx := range []int{1, 2} {
		names := toolNames(mock.calls[idx].Tools)
		if len(names) != fullToolCount {
			t.Errorf("call[%d] tool count = %d, want %d; tools: %v", idx, len(names), fullToolCount, names)
		}
	}
}

func TestIter0ToolGating_DisabledWhenNoDelegation(t *testing.T) {
	// When iter0Tools is set but thane_delegate is NOT in the registry,
	// gating should be auto-disabled — all tools visible on iter-0.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:   "test-model",
				Message: llm.Message{Role: "assistant", Content: "No delegation available."},
			},
		},
	}

	// Note: thane_delegate is NOT in the registry (not in extraNames).
	loop := buildTestLoop(mock, []string{"recall_fact"})
	fullToolCount := len(loop.tools.List())
	loop.SetIter0Tools([]string{"thane_delegate", "recall_fact"})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "test"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(mock.calls))
	}

	// All tools should be visible because gating is disabled (no thane_delegate).
	names := toolNames(mock.calls[0].Tools)
	if len(names) != fullToolCount {
		t.Errorf("iter-0 tool count = %d, want %d (gating should be disabled); tools: %v", len(names), fullToolCount, names)
	}
}

func TestIter0ToolGating_DisabledWhenEmpty(t *testing.T) {
	// When iter0Tools is nil/empty, all tools should be available on iter-0.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:   "test-model",
				Message: llm.Message{Role: "assistant", Content: "All tools available."},
			},
		},
	}

	loop := buildTestLoop(mock, []string{"thane_delegate", "recall_fact"})
	fullToolCount := len(loop.tools.List())
	// Don't call SetIter0Tools — leave nil.

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "test"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	names := toolNames(mock.calls[0].Tools)
	if len(names) != fullToolCount {
		t.Errorf("iter-0 tool count = %d, want %d; tools: %v", len(names), fullToolCount, names)
	}
}
