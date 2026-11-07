package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/runtime/iterate"
	"github.com/nugget/thane-ai-agent/internal/tools"
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

// --- toolsUsedFromMessages tests ---

func TestToolsUsedFromMessages(t *testing.T) {
	t.Parallel()

	mkCall := func(id, name string) llm.ToolCall {
		tc := llm.ToolCall{ID: id}
		tc.Function.Name = name
		return tc
	}

	msgs := []llm.Message{
		{
			Role:      "assistant",
			ToolCalls: []llm.ToolCall{mkCall("tc1", "file_write"), mkCall("tc2", "file_edit")},
		},
		{Role: "tool", ToolCallID: "tc1", Content: "ok"},
		{Role: "tool", ToolCallID: "tc2", Content: "ok"},
		{
			Role:      "assistant",
			ToolCalls: []llm.ToolCall{mkCall("tc3", "file_write"), mkCall("tc4", "file_write")},
		},
		{Role: "tool", ToolCallID: "tc3", Content: "ok"},
		// tc4 has no result — should not be counted.
	}

	used := toolsUsedFromMessages(msgs)

	if used["file_write"] != 2 {
		t.Errorf("file_write = %d, want 2", used["file_write"])
	}
	if used["file_edit"] != 1 {
		t.Errorf("file_edit = %d, want 1", used["file_edit"])
	}
	if len(used) != 2 {
		t.Errorf("len(used) = %d, want 2", len(used))
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

// TestTimeoutRecovery_LaterCallKeepsRunModel pins the model a run is
// recorded under when a call after timeout recovery's reply ends it.
// Recovery answered one call, input arriving at closure held the turn
// open, and the next call went back to the run's own model and ended
// the run there, by failing or by spending the rest of the output
// budget. The request record, and the Response when the run completes,
// name that model, not the recovery model whose response the run
// reported last.
func TestTimeoutRecovery_LaterCallKeepsRunModel(t *testing.T) {
	t.Parallel()

	failure := errors.New("provider unavailable")
	tests := []struct {
		name string
		// maxOutputTokens is the request's output budget; zero is
		// none.
		maxOutputTokens int
		// laterOutputTokens is what the sixth call, on the run's own
		// model, spends before it fails with laterErr.
		laterOutputTokens int
		laterErr          error
		// wantErr is the error Run fails with; nil when it completes.
		wantErr    error
		wantFinish string
	}{
		{
			// The explicit request model rules failover out, so the
			// failure ends the run.
			name:     "fails answering input at closure",
			laterErr: failure,
			wantErr:  failure,
		},
		{
			name:              "spends the budget answering input at closure",
			maxOutputTokens:   50,
			laterOutputTokens: 20,
			laterErr:          errors.New("stream reset"),
			wantFinish:        iterate.ExhaustTokenBudget,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			loop, _, _ := newAccountingTestLoop(t)
			loop.recoveryModel = "claude-recovery"
			var dispatched []string
			loop.llm = accountingTestClient{call: func(_ context.Context, model string, _ []map[string]any) (*llm.ChatResponse, error) {
				dispatched = append(dispatched, model)
				switch len(dispatched) {
				case 1:
					return accountingToolResponse(), nil
				case 2, 3, 4:
					return nil, errTestTimeout
				case 5:
					return &llm.ChatResponse{
						Model: model, StopReason: "end_turn", OutputTokens: 20,
						Message: llm.Message{Role: "assistant", Content: "Recovered."},
					}, nil
				case 6:
					if tt.laterOutputTokens > 0 {
						return &llm.ChatResponse{Model: model, OutputTokens: tt.laterOutputTokens}, tt.laterErr
					}
					return nil, tt.laterErr
				default:
					return nil, fmt.Errorf("unexpected model call %d", len(dispatched))
				}
			}}
			delivered := false
			pullInput := func(context.Context) []llm.Message {
				if delivered || len(dispatched) < 5 {
					return nil
				}
				delivered = true
				return []llm.Message{{Role: "user", Content: "And the tests?"}}
			}
			var live logging.RequestContent
			loop.liveRequestRecorder = func(_ context.Context, content logging.RequestContent) { live = content }

			resp, err := loop.Run(context.Background(), &Request{
				ConversationID:  "later-call-after-recovery",
				Model:           "claude-primary",
				MaxOutputTokens: tt.maxOutputTokens,
				Messages:        []Message{{Role: "user", Content: "Inspect the source."}},
				PullInput:       pullInput,
			}, nil)
			want := []string{"claude-primary", "claude-primary", "claude-primary", "claude-primary", "claude-recovery", "claude-primary"}
			if !slices.Equal(dispatched, want) {
				t.Errorf("dispatched models = %v, want %v", dispatched, want)
			}
			if live.Model != "claude-primary" {
				t.Errorf("live request model = %q, want claude-primary", live.Model)
			}
			if tt.wantErr != nil {
				if resp != nil || !errors.Is(err, tt.wantErr) {
					t.Fatalf("Run = %+v, %v; want the failed call's error", resp, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if resp.FinishReason != tt.wantFinish {
				t.Errorf("finish reason = %q, want %q", resp.FinishReason, tt.wantFinish)
			}
			if resp.Model != "claude-primary" {
				t.Errorf("response model = %q, want claude-primary", resp.Model)
			}
			// The run ended on the runtime's placeholder, not on the
			// recovered reply.
			if resp.Content != prompts.EmptyResponseFallback {
				t.Errorf("content = %q, want the empty-response fallback", resp.Content)
			}
		})
	}
}

// TestTimeoutRecovery_ReplyToolCallsDoNotRun pins that a timeout
// recovery reply runs no tool calls. The recovery request offers no
// tools, but its reply can still carry calls: an OpenAI-compatible
// runner's text fallback recovers a call the model wrote as text under
// any name when the request offered none, and the engine would run it
// against the run's tools, on a summarizer's word, after the turn had
// already timed out. The reply keeps its own text, or takes
// TimeoutRecoveryEmpty when the fallback consumed it, and still ends
// the turn as timeout recovery's. The response line reports what the
// fallback did, and a warning keeps the dropped calls' provenance.
func TestTimeoutRecovery_ReplyToolCallsDoNotRun(t *testing.T) {
	t.Parallel()

	sendReport := llm.ToolCall{ID: "recovery-call"}
	sendReport.Function.Name = "send_report"
	sendReport.Function.Arguments = map[string]any{}

	tests := []struct {
		name string
		// reply is the recovery model's message, which goes through the
		// text fallback with the recovery request's tool names, as an
		// OpenAI-compatible runner's reply does.
		reply       llm.Message
		wantContent string
		// wantResponse is the recovered reply's llm response line.
		wantResponse map[string]any
		// wantDropped is the warning that records the dropped calls.
		wantDropped map[string]any
	}{
		{
			name:        "call written as text",
			reply:       llm.Message{Role: "assistant", Content: `{"name":"send_report","arguments":{}}`},
			wantContent: prompts.TimeoutRecoveryEmpty,
			wantResponse: map[string]any{
				"tool_calls": float64(0), "tool_call_source": "", "text_tool_action": "recovered", "profile": "",
			},
			wantDropped: map[string]any{
				"dropped_tool_calls": float64(1), "tool_call_source": "text_fallback", "text_tool_action": "recovered",
			},
		},
		{
			name: "structured call beside text",
			reply: llm.Message{
				Role: "assistant", Content: "Inspected the source; sending the report.",
				ToolCalls: []llm.ToolCall{sendReport},
			},
			wantContent: "Inspected the source; sending the report.",
			wantResponse: map[string]any{
				"tool_calls": float64(0), "tool_call_source": "", "text_tool_action": "none", "profile": "",
			},
			wantDropped: map[string]any{
				"dropped_tool_calls": float64(1), "tool_call_source": "native", "text_tool_action": "none",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			loop, _, _ := newAccountingTestLoop(t)
			loop.recoveryModel = "claude-recovery"
			var ran atomic.Bool
			loop.tools.Register(&tools.Tool{
				Name: "send_report", Description: "Send the operator a report.",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
				Handler: func(context.Context, map[string]any) (string, error) {
					ran.Store(true)
					return "report sent", nil
				},
			})
			var dispatched []string
			loop.llm = accountingTestClient{call: func(_ context.Context, model string, defs []map[string]any) (*llm.ChatResponse, error) {
				dispatched = append(dispatched, model)
				switch len(dispatched) {
				case 1:
					return accountingToolResponse(), nil
				case 2, 3, 4:
					return nil, errTestTimeout
				case 5:
					if len(defs) != 0 {
						t.Errorf("recovery request offered %d tools, want none", len(defs))
					}
					resp := &llm.ChatResponse{Model: model, StopReason: "stop", Message: tt.reply}
					llm.ApplyTextToolCallFallback(resp, llm.ExtractToolNames(defs), llm.DefaultToolCallTextProfile())
					return resp, nil
				default:
					// Answer anything the run sends after recovery, so a
					// run that ran the calls ends and shows it.
					return &llm.ChatResponse{
						Model: model, StopReason: "end_turn",
						Message: llm.Message{Role: "assistant", Content: "Answered after the recovery reply."},
					}, nil
				}
			}}
			logs := &lockedLogBuffer{}
			ctx := logging.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(logs, nil)))

			resp, err := loop.Run(ctx, &Request{
				ConversationID: "recovery-tool-calls",
				Model:          "claude-primary",
				Messages:       []Message{{Role: "user", Content: "Inspect the source."}},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if ran.Load() {
				t.Error("send_report ran from the timeout recovery reply")
			}
			want := []string{"claude-primary", "claude-primary", "claude-primary", "claude-primary", "claude-recovery"}
			if !slices.Equal(dispatched, want) {
				t.Errorf("dispatched models = %v, want %v", dispatched, want)
			}
			if resp.Content != tt.wantContent {
				t.Errorf("content = %q, want %q", resp.Content, tt.wantContent)
			}
			if resp.FinishReason != "timeout_recovery" {
				t.Errorf("finish reason = %q, want timeout_recovery", resp.FinishReason)
			}
			if resp.Model != "claude-recovery" {
				t.Errorf("response model = %q, want claude-recovery", resp.Model)
			}

			lines := logs.records(t, "llm response")
			if len(lines) != 2 {
				t.Fatalf("llm response lines = %d, want 2: %v", len(lines), lines)
			}
			assertLogFields(t, "recovered reply's llm response line", lines[1], tt.wantResponse)
			dropped := logs.records(t, "dropped tool calls from timeout recovery reply")
			if len(dropped) != 1 {
				t.Fatalf("dropped tool call warnings = %d, want 1", len(dropped))
			}
			wantDropped := maps.Clone(tt.wantDropped)
			wantDropped["level"] = "WARN"
			wantDropped["recovery_model"] = "claude-recovery"
			wantDropped["dropped_tools"] = []any{"send_report"}
			assertLogFields(t, "dropped tool call warning", dropped[0], wantDropped)
		})
	}
}

// assertLogFields reports each field of want that rec, a decoded JSON
// log record named what, lacks or holds a different value for.
func assertLogFields(t *testing.T, what string, rec, want map[string]any) {
	t.Helper()
	for key, value := range want {
		got, ok := rec[key]
		if !ok {
			t.Errorf("%s missing %q: %v", what, key, rec)
			continue
		}
		if !reflect.DeepEqual(got, value) {
			t.Errorf("%s %s = %v, want %v", what, key, got, value)
		}
	}
}

func TestAmbiguousModelError_DoesNotFailOver(t *testing.T) {
	t.Parallel()

	mock := &mockTimeoutLLM{
		errors: []error{
			&llm.AmbiguousModelError{
				Model:   "gpt-oss:20b",
				Targets: []string{"mirror/gpt-oss:20b", "spark/gpt-oss:20b"},
			},
		},
	}

	loop := buildTestLoopWithLLM(mock, nil)
	loop.model = "spark/gpt-oss:20b"

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "Reply with exactly ok"}},
		Model:    "gpt-oss:20b",
	}, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want ambiguity error")
	}
	if !strings.Contains(err.Error(), `model "gpt-oss:20b" is ambiguous`) {
		t.Fatalf("Run() error = %q, want ambiguity message", err)
	}

	mock.mu.Lock()
	callCount := len(mock.calls)
	mock.mu.Unlock()
	if callCount != 1 {
		t.Fatalf("LLM call count = %d, want 1 (no failover)", callCount)
	}
}

func TestExplicitModelError_DoesNotFailOver(t *testing.T) {
	t.Parallel()

	mock := &mockTimeoutLLM{
		errors: []error{
			errors.New("runner returned empty completion"),
		},
	}

	loop := buildTestLoopWithLLM(mock, nil)
	loop.model = "spark/gpt-oss:20b"

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "Reply with exactly ok"}},
		Model:    "deepslate/google/gemma-3-4b",
	}, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want provider error")
	}
	if !strings.Contains(err.Error(), "empty completion") {
		t.Fatalf("Run() error = %q, want empty completion error", err)
	}

	mock.mu.Lock()
	callCount := len(mock.calls)
	mock.mu.Unlock()
	if callCount != 1 {
		t.Fatalf("LLM call count = %d, want 1 (no failover)", callCount)
	}
}

func TestCanceledContext_DoesNotFailOver(t *testing.T) {
	t.Parallel()

	mock := &mockTimeoutLLM{}
	loop := buildTestLoopWithLLM(mock, nil)
	loop.model = "fallback-model"

	reqCtx, cancelReq := context.WithCancel(context.Background())
	cancelReq()
	iterCtx, cancelIter := context.WithCancel(context.Background())
	cancelIter()

	var outcome llmErrorOutcome
	handler := loop.buildLLMErrorHandler(reqCtx, nil, loop.llm, &Request{}, &outcome)

	_, _, err := handler(
		iterCtx,
		errors.New(`request failed: Post "https://api.anthropic.com/v1/messages": context canceled`),
		"claude-opus-4-20250514",
		nil,
		nil,
		nil,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("handler error = %v, want context canceled", err)
	}

	mock.mu.Lock()
	callCount := len(mock.calls)
	mock.mu.Unlock()
	if callCount != 0 {
		t.Fatalf("LLM call count = %d, want 0 (no failover)", callCount)
	}
}

func TestProviderPrivateCancellationCanFailOver(t *testing.T) {
	t.Parallel()

	mock := &mockTimeoutLLM{
		responses: []*llm.ChatResponse{{
			Model:   "fallback-model",
			Message: llm.Message{Role: "assistant", Content: "Recovered through provider failover."},
		}},
	}
	loop := buildTestLoopWithLLM(mock, nil)
	loop.model = "fallback-model"

	var outcome llmErrorOutcome
	handler := loop.buildLLMErrorHandler(context.Background(), nil, loop.llm, &Request{}, &outcome)

	resp, model, err := handler(
		context.Background(),
		fmt.Errorf("provider stream guard: %w", context.Canceled),
		"stalled-model",
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("handler error = %v, want provider failover", err)
	}
	if resp == nil || model != "fallback-model" {
		t.Fatalf("handler result = resp %#v model %q, want fallback-model response", resp, model)
	}

	mock.mu.Lock()
	callCount := len(mock.calls)
	mock.mu.Unlock()
	if callCount != 1 {
		t.Fatalf("LLM call count = %d, want 1 failover", callCount)
	}
}

func TestRoutedUserFixableAPIError_DoesNotFailOver(t *testing.T) {
	t.Parallel()

	mock := &mockTimeoutLLM{
		errors: []error{
			errors.New(`API error 400: {"error":"Invalid image detected at index 0"}`),
		},
	}

	loop := buildTestLoopWithLLM(mock, nil)
	loop.model = "spark/gpt-oss:20b"

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{
			Role:    "user",
			Content: "describe the image",
			Images:  []llm.ImageContent{{Data: "Zm9v", MediaType: "image/png"}},
		}},
	}, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want provider 400 error")
	}
	if !strings.Contains(err.Error(), "API error 400:") {
		t.Fatalf("Run() error = %q, want provider 400 error", err)
	}

	mock.mu.Lock()
	callCount := len(mock.calls)
	mock.mu.Unlock()
	if callCount != 1 {
		t.Fatalf("LLM call count = %d, want 1 (no failover)", callCount)
	}
}

// buildTestLoopWithLLM creates a test Loop with a custom LLM client
// and a near-zero retry delay so tests don't block on real backoff.
func buildTestLoopWithLLM(client llm.Client, extraNames []string) *Loop {
	loop := buildTestLoop(&mockLLM{}, extraNames)
	loop.llm = client
	loop.retryBaseDelay = 1 * time.Millisecond // fast retries in tests
	return loop
}
