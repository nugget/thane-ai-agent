package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/llm"
)

// --- isTimeout tests ---

func TestIsTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"wrapped deadline", fmt.Errorf("llm: %w", context.DeadlineExceeded), true},
		{"timeout string", errors.New("request timeout"), true},
		{"overloaded", errors.New("server overloaded"), true},
		{"529 status", errors.New("HTTP 529: overloaded"), true},
		{"connection refused", errors.New("connection refused"), false},
		{"auth error", errors.New("401 unauthorized"), false},
		{"generic error", errors.New("something broke"), false},
		{"cancelled", context.Canceled, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isTimeout(tt.err)
			if got != tt.want {
				t.Errorf("isTimeout(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// --- buildRecoveryPrompt tests ---

func TestBuildRecoveryPrompt(t *testing.T) {
	t.Parallel()

	messages := []llm.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Move these files"},
		{Role: "assistant", Content: "I'll move them now."},
		{Role: "tool", Content: "wrote config.yaml successfully", ToolCallID: "call-1"},
		{Role: "tool", Content: "Error: file not found", ToolCallID: "call-2"},
		{Role: "tool", Content: strings.Repeat("x", 300), ToolCallID: "call-3"},
	}
	toolsUsed := map[string]int{
		"file_write": 2,
		"file_read":  1,
	}

	result := buildRecoveryPrompt(messages, toolsUsed)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}

	if result[0].Role != "system" {
		t.Errorf("first message role = %q, want system", result[0].Role)
	}
	if result[1].Role != "user" {
		t.Errorf("second message role = %q, want user", result[1].Role)
	}

	body := result[1].Content

	// Should mention success and error
	if !strings.Contains(body, "[success]") {
		t.Error("recovery prompt should contain [success] status")
	}
	if !strings.Contains(body, "[error]") {
		t.Error("recovery prompt should contain [error] status")
	}

	// Long tool result should be truncated
	if strings.Contains(body, strings.Repeat("x", 300)) {
		t.Error("long tool result should be truncated")
	}
	if !strings.Contains(body, "...") {
		t.Error("truncated tool result should end with ...")
	}

	// Should include tool counts
	if !strings.Contains(body, "file_write") || !strings.Contains(body, "file_read") {
		t.Error("recovery prompt should include tool names in counts")
	}
}

// --- staticRecoveryResponse tests ---

func TestStaticRecoveryResponse(t *testing.T) {
	t.Parallel()

	toolsUsed := map[string]int{
		"file_write": 3,
		"file_edit":  2,
	}

	resp := staticRecoveryResponse(toolsUsed, "test-model", 1000, 500, "sess-1", "req-1")

	if resp.FinishReason != "timeout_recovery" {
		t.Errorf("FinishReason = %q, want timeout_recovery", resp.FinishReason)
	}
	if !strings.Contains(resp.Content, "5 tool call") {
		t.Errorf("content should mention 5 tool calls, got: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "file_write") {
		t.Errorf("content should mention file_write, got: %s", resp.Content)
	}
	if resp.Model != "test-model" {
		t.Errorf("Model = %q, want test-model", resp.Model)
	}
	if resp.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", resp.SessionID)
	}
}

// --- Integration: timeout retry + recovery ---

// mockTimeoutLLM extends mockLLM to return errors for specific call indices.
type mockTimeoutLLM struct {
	mu        sync.Mutex
	responses []*llm.ChatResponse // responses for successful calls
	errors    []error             // per-call errors (nil = use response)
	callIndex int
	calls     []mockLLMCall
}

func (m *mockTimeoutLLM) Chat(_ context.Context, model string, msgs []llm.Message, td []map[string]any) (*llm.ChatResponse, error) {
	return m.ChatStream(context.Background(), model, msgs, td, nil)
}

func (m *mockTimeoutLLM) ChatStream(_ context.Context, model string, msgs []llm.Message, td []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls = append(m.calls, mockLLMCall{Model: model, Messages: msgs, Tools: td})

	idx := m.callIndex
	m.callIndex++

	if idx < len(m.errors) && m.errors[idx] != nil {
		return nil, m.errors[idx]
	}

	respIdx := 0
	for i := 0; i <= idx; i++ {
		if i >= len(m.errors) || m.errors[i] == nil {
			if i == idx {
				break
			}
			respIdx++
		}
	}

	if respIdx >= len(m.responses) {
		return nil, fmt.Errorf("mockTimeoutLLM: no more responses (respIdx %d)", respIdx)
	}
	return m.responses[respIdx], nil
}

func (m *mockTimeoutLLM) Ping(_ context.Context) error { return nil }

func TestTimeoutRetry_SucceedsOnSecondAttempt(t *testing.T) {
	t.Parallel()

	mock := &mockTimeoutLLM{
		responses: []*llm.ChatResponse{
			// Iter 0: tool call (succeeds)
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
							Name:      "recall_fact",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 50,
			},
			// Iter 1 attempt 1: timeout (error injected below)
			// Iter 1 retry 1: succeeds
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Here's the result."},
				InputTokens:  200,
				OutputTokens: 20,
			},
		},
		errors: []error{
			nil,                      // call 0: tool call succeeds
			context.DeadlineExceeded, // call 1: first attempt times out
			nil,                      // call 2: retry succeeds
		},
	}

	loop := buildTestLoopWithLLM(mock, []string{"recall_fact"})

	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "recall something"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if resp.Content != "Here's the result." {
		t.Errorf("content = %q, want %q", resp.Content, "Here's the result.")
	}

	// Should have made 3 LLM calls: initial tool call, timeout, retry success
	mock.mu.Lock()
	callCount := len(mock.calls)
	mock.mu.Unlock()
	if callCount != 3 {
		t.Errorf("expected 3 LLM calls, got %d", callCount)
	}
}

func TestTimeoutRecovery_DownshiftsToRecoveryModel(t *testing.T) {
	t.Parallel()

	mock := &mockTimeoutLLM{
		responses: []*llm.ChatResponse{
			// Iter 0: tool call (succeeds)
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
							Name:      "recall_fact",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 50,
			},
			// Recovery model response
			{
				Model:        "recovery-model",
				Message:      llm.Message{Role: "assistant", Content: "I completed 1 tool call before timing out."},
				InputTokens:  50,
				OutputTokens: 15,
			},
		},
		errors: []error{
			nil,                      // call 0: tool call succeeds
			context.DeadlineExceeded, // call 1: timeout
			context.DeadlineExceeded, // call 2: retry 1 timeout
			context.DeadlineExceeded, // call 3: retry 2 timeout
			nil,                      // call 4: recovery model succeeds
		},
	}

	loop := buildTestLoopWithLLM(mock, []string{"recall_fact"})
	loop.recoveryModel = "recovery-model"

	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "recall something"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if resp.FinishReason != "timeout_recovery" {
		t.Errorf("FinishReason = %q, want timeout_recovery", resp.FinishReason)
	}

	if resp.Content != "I completed 1 tool call before timing out." {
		t.Errorf("content = %q, unexpected", resp.Content)
	}

	// Verify recovery model was used
	mock.mu.Lock()
	lastCall := mock.calls[len(mock.calls)-1]
	mock.mu.Unlock()
	if lastCall.Model != "recovery-model" {
		t.Errorf("last call model = %q, want recovery-model", lastCall.Model)
	}
}

func TestTimeoutRecovery_StaticFallbackWhenNoRecoveryModel(t *testing.T) {
	t.Parallel()

	mock := &mockTimeoutLLM{
		responses: []*llm.ChatResponse{
			// Iter 0: tool call (succeeds)
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
							Name:      "recall_fact",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 50,
			},
		},
		errors: []error{
			nil,                      // call 0: tool call succeeds
			context.DeadlineExceeded, // call 1: timeout
			context.DeadlineExceeded, // call 2: retry 1 timeout
			context.DeadlineExceeded, // call 3: retry 2 timeout
		},
	}

	loop := buildTestLoopWithLLM(mock, []string{"recall_fact"})
	// No recovery model set

	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "recall something"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if resp.FinishReason != "timeout_recovery" {
		t.Errorf("FinishReason = %q, want timeout_recovery", resp.FinishReason)
	}

	if !strings.Contains(resp.Content, "tool call") {
		t.Errorf("static fallback should mention tool calls, got: %s", resp.Content)
	}
}

// buildTestLoopWithLLM creates a test Loop with a custom LLM client.
func buildTestLoopWithLLM(client llm.Client, extraNames []string) *Loop {
	loop := buildTestLoop(&mockLLM{}, extraNames)
	loop.llm = client
	return loop
}
