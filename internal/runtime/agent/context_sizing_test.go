package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func sizingToolCall(id, name string, args map[string]any) llm.ToolCall {
	tc := llm.ToolCall{ID: id}
	tc.Function.Name = name
	tc.Function.Arguments = args
	return tc
}

func TestEstimateRequest(t *testing.T) {
	t.Parallel()

	toolDefs := []map[string]any{{
		"name":        "lookup",
		"description": strings.Repeat("Look up a record by key. ", 40),
		"parameters":  map[string]any{"type": "object"},
	}}
	args := map[string]any{"query": strings.Repeat("q", 2000)}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	encodedDefs, err := json.Marshal(toolDefs)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name          string
		msgs          []llm.Message
		toolDefs      []map[string]any
		wantMessages  int
		wantToolCalls int
		wantTools     int
	}{
		{
			name:         "text only",
			msgs:         []llm.Message{{Role: "system", Content: "abcdefgh"}, {Role: "user", Content: "ijkl"}},
			wantMessages: 3,
		},
		{
			name:         "tool schemas count beside the messages",
			msgs:         []llm.Message{{Role: "user", Content: "ijkl"}},
			toolDefs:     toolDefs,
			wantMessages: 1,
			wantTools:    roughTokenCount(string(encodedDefs)),
		},
		{
			name: "tool-call arguments count as tool calls",
			msgs: []llm.Message{
				{Role: "assistant", ToolCalls: []llm.ToolCall{sizingToolCall("abcd", "lookup", args)}},
			},
			wantToolCalls: 1 + 2 + roughTokenCount(string(encodedArgs)),
		},
		{
			name:         "a tool result's call id counts as message content",
			msgs:         []llm.Message{{Role: "tool", Content: "ok", ToolCallID: "abcd"}},
			wantMessages: 1 + 1,
		},
		{
			name:         "images count at the fixed per-image estimate",
			msgs:         []llm.Message{{Role: "user", Content: "look", Images: []llm.ImageContent{{}, {}}}},
			wantMessages: 1 + 2*estimatedImageContextTokens,
		},
		{
			name: "every component together",
			msgs: []llm.Message{
				{Role: "user", Content: "ijkl"},
				{Role: "assistant", ToolCalls: []llm.ToolCall{sizingToolCall("abcd", "lookup", args)}},
				{Role: "tool", Content: "ok", ToolCallID: "abcd"},
			},
			toolDefs:      toolDefs,
			wantMessages:  1 + 1 + 1,
			wantToolCalls: 1 + 2 + roughTokenCount(string(encodedArgs)),
			wantTools:     roughTokenCount(string(encodedDefs)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := estimateRequest(tt.msgs, tt.toolDefs)
			want := requestEstimate{Messages: tt.wantMessages, ToolCalls: tt.wantToolCalls, Tools: tt.wantTools}
			if got != want {
				t.Fatalf("estimateRequest() = %+v, want %+v", got, want)
			}
			if total := estimateRequestContextTokens(tt.msgs, tt.toolDefs); total != got.total() {
				t.Errorf("estimateRequestContextTokens() = %d, want the estimate's total %d", total, got.total())
			}
			if msgsOnly := estimateLLMMessagesContextTokens(tt.msgs); msgsOnly != got.Messages+got.ToolCalls {
				t.Errorf("estimateLLMMessagesContextTokens() = %d, want %d", msgsOnly, got.Messages+got.ToolCalls)
			}
		})
	}
}

// sentRequest is what a model call actually carried, measured as it was
// sent so later in-place edits to the message slice cannot change it.
type sentRequest struct {
	estimate requestEstimate
	legacy   int            // the legacy est_tokens figure: message text bytes / 4
	sections map[string]int // system prompt section bytes by name
}

// estimatingClient records the size of every request it forwards.
type estimatingClient struct {
	llm.Client
	mu   sync.Mutex
	sent []sentRequest
}

func (c *estimatingClient) Chat(ctx context.Context, model string, msgs []llm.Message, td []map[string]any) (*llm.ChatResponse, error) {
	return c.ChatStream(ctx, model, msgs, td, nil)
}

func (c *estimatingClient) ChatStream(ctx context.Context, model string, msgs []llm.Message, td []map[string]any, cb llm.StreamCallback) (*llm.ChatResponse, error) {
	req := sentRequest{estimate: estimateRequest(msgs, td), sections: map[string]int{}}
	for _, m := range msgs {
		req.legacy += len(m.Content) / 4
	}
	if len(msgs) > 0 {
		for _, s := range msgs[0].Sections {
			req.sections[s.Name] += len(s.Content)
		}
	}
	c.mu.Lock()
	c.sent = append(c.sent, req)
	c.mu.Unlock()
	return c.Client.ChatStream(ctx, model, msgs, td, cb)
}

func (c *estimatingClient) requests() []sentRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sentRequest(nil), c.sent...)
}

// lockedBuffer is a bytes.Buffer safe for a log handler shared with
// goroutines the Run starts.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// records returns the JSON log records with the given message, keyed by
// their iteration attribute.
func (b *lockedBuffer) records(t *testing.T, msg string) map[int]map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[int]map[string]any)
	for _, line := range bytes.Split(b.buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("unparseable log line %q: %v", line, err)
		}
		if rec["msg"] != msg {
			continue
		}
		iter, ok := rec["iteration"].(float64)
		if !ok {
			t.Fatalf("%s record without an iteration: %v", msg, rec)
		}
		if _, dup := out[int(iter)]; dup {
			t.Fatalf("two %s records for iteration %d", msg, int(iter))
		}
		out[int(iter)] = rec
	}
	return out
}

func intAttr(t *testing.T, rec map[string]any, key string) int {
	t.Helper()
	v, ok := rec[key].(float64)
	if !ok {
		t.Fatalf("%s missing or not a number in %v", key, rec)
	}
	return int(v)
}

// TestRun_RequestEstimateMatchesWhatIsSent proves the per-call estimate is
// taken from the request the model receives — tool schemas and tool-call
// arguments included — and that the log line, the stream event and the
// response line all report that one number.
func TestRun_RequestEstimateMatchesWhatIsSent(t *testing.T) {
	t.Parallel()

	bigArgs := map[string]any{"query": strings.Repeat("q", 4000)}
	// The engine rewrites a response's tool calls in place, so every
	// subtest gets responses of its own.
	toolCallResp := func() *llm.ChatResponse {
		return &llm.ChatResponse{
			Model: "test-model",
			Message: llm.Message{
				Role:      "assistant",
				ToolCalls: []llm.ToolCall{sizingToolCall("call-1", "lookup", bigArgs)},
			},
			InputTokens: 100, OutputTokens: 10,
		}
	}
	textResp := func() *llm.ChatResponse {
		return &llm.ChatResponse{
			Model:       "test-model",
			Message:     llm.Message{Role: "assistant", Content: "done"},
			InputTokens: 200, OutputTokens: 5,
		}
	}
	recoveryResp := func() *llm.ChatResponse {
		return &llm.ChatResponse{
			Model:       "recovery-model",
			Message:     llm.Message{Role: "assistant", Content: "recovered"},
			InputTokens: 50, OutputTokens: 5,
		}
	}

	tests := []struct {
		name          string
		model         llm.Client
		recoveryModel string
		// callsByIteration maps each iteration to the indices of the
		// model calls that carried its request: more than one when the
		// iteration was retried.
		callsByIteration map[int][]int
		// responseEstimate says whether an iteration's response line
		// must carry est_request_tokens.
		responseEstimate map[int]bool
	}{
		{
			name:             "tool schemas and tool-call arguments",
			model:            &mockLLM{responses: []*llm.ChatResponse{toolCallResp(), textResp()}},
			callsByIteration: map[int][]int{0: {0}, 1: {1}},
			responseEstimate: map[int]bool{0: true, 1: true},
		},
		{
			name: "timeout retry resends the estimated request",
			model: &mockTimeoutLLM{
				responses: []*llm.ChatResponse{toolCallResp(), textResp()},
				errors:    []error{nil, context.DeadlineExceeded, nil},
			},
			callsByIteration: map[int][]int{0: {0}, 1: {1, 2}},
			responseEstimate: map[int]bool{0: true, 1: true},
		},
		{
			name: "timeout recovery answers a different request",
			model: &mockTimeoutLLM{
				responses: []*llm.ChatResponse{toolCallResp(), recoveryResp()},
				errors:    []error{nil, context.DeadlineExceeded, context.DeadlineExceeded, context.DeadlineExceeded, nil},
			},
			recoveryModel:    "recovery-model",
			callsByIteration: map[int][]int{0: {0}, 1: {1, 2, 3}},
			responseEstimate: map[int]bool{0: true, 1: false},
		},
		{
			// The recovery call fails too, and the handler answers
			// with canned text no model produced.
			name: "recovery model also fails",
			model: &mockTimeoutLLM{
				responses: []*llm.ChatResponse{toolCallResp()},
				errors:    []error{nil, context.DeadlineExceeded, context.DeadlineExceeded, context.DeadlineExceeded, context.DeadlineExceeded},
			},
			recoveryModel:    "recovery-model",
			callsByIteration: map[int][]int{0: {0}, 1: {1, 2, 3}},
			responseEstimate: map[int]bool{0: true, 1: false},
		},
		{
			name: "no recovery model answers with a static fallback",
			model: &mockTimeoutLLM{
				responses: []*llm.ChatResponse{toolCallResp()},
				errors:    []error{nil, context.DeadlineExceeded, context.DeadlineExceeded, context.DeadlineExceeded},
			},
			callsByIteration: map[int][]int{0: {0}, 1: {1, 2, 3}},
			responseEstimate: map[int]bool{0: true, 1: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &estimatingClient{Client: tt.model}
			loop := buildTestLoopWithLLM(client, nil)
			loop.recoveryModel = tt.recoveryModel
			loop.tools.Register(&tools.Tool{
				Name:        "lookup",
				Description: strings.Repeat("Look up a record by key. ", 200),
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
				Handler: func(context.Context, map[string]any) (string, error) {
					return "ok", nil
				},
			})

			var logs lockedBuffer
			ctx := logging.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)))
			var streamMu sync.Mutex
			streamed := make(map[int]any)
			stream := func(ev llm.StreamEvent) {
				if ev.Kind != llm.KindLLMStart {
					return
				}
				streamMu.Lock()
				defer streamMu.Unlock()
				streamed[ev.Data["iteration"].(int)] = ev.Data["est_tokens"]
			}

			if _, err := loop.Run(ctx, &Request{
				Messages: []Message{{Role: "user", Content: "look up the record"}},
			}, stream); err != nil {
				t.Fatalf("Run() error: %v", err)
			}

			sent := client.requests()
			calls := logs.records(t, "llm call")
			responses := logs.records(t, "llm response")
			if len(calls) != len(tt.callsByIteration) {
				t.Fatalf("llm call lines = %d, want %d", len(calls), len(tt.callsByIteration))
			}

			for iter, callIdx := range tt.callsByIteration {
				want := sent[callIdx[0]]
				for _, idx := range callIdx[1:] {
					if sent[idx].estimate != want.estimate {
						t.Errorf("iteration %d: call %d sent %+v, call %d sent %+v; a retry must resend the request",
							iter, callIdx[0], want.estimate, idx, sent[idx].estimate)
					}
				}

				call := calls[iter]
				if got := intAttr(t, call, "est_request_tokens"); got != want.estimate.total() {
					t.Errorf("iteration %d: est_request_tokens = %d, want %d (what was sent)", iter, got, want.estimate.total())
				}
				if got := intAttr(t, call, "est_tool_tokens"); got != want.estimate.Tools || got == 0 {
					t.Errorf("iteration %d: est_tool_tokens = %d, want %d and nonzero", iter, got, want.estimate.Tools)
				}
				if got := intAttr(t, call, "est_tool_call_tokens"); got != want.estimate.ToolCalls {
					t.Errorf("iteration %d: est_tool_call_tokens = %d, want %d", iter, got, want.estimate.ToolCalls)
				}
				if iter > 0 && want.estimate.ToolCalls < len(bigArgs["query"].(string))/4 {
					t.Errorf("iteration %d: tool-call estimate %d does not cover the %d-byte arguments",
						iter, want.estimate.ToolCalls, len(bigArgs["query"].(string)))
				}
				if got := intAttr(t, call, "est_tokens"); got != want.legacy {
					t.Errorf("iteration %d: est_tokens = %d, want the legacy message-text figure %d", iter, got, want.legacy)
				}

				streamMu.Lock()
				streamValue, ok := streamed[iter]
				streamMu.Unlock()
				if !ok || streamValue != want.estimate.total() {
					t.Errorf("iteration %d: stream est_tokens = %v, want the log line's est_request_tokens %d",
						iter, streamValue, want.estimate.total())
				}

				sections, hasSections := call["system_section_bytes"]
				switch {
				case iter == 0 && !hasSections:
					t.Errorf("iteration 0: no system_section_bytes on the llm call line")
				case iter == 0:
					got := map[string]int{}
					for name, v := range sections.(map[string]any) {
						got[name] = int(v.(float64))
					}
					if len(got) == 0 || !maps.Equal(got, want.sections) {
						t.Errorf("iteration 0: system_section_bytes = %v, want %v", got, want.sections)
					}
				case hasSections:
					t.Errorf("iteration %d: system_section_bytes = %v, want it on iteration 0 only", iter, sections)
				}

				resp, ok := responses[iter]
				if !ok {
					t.Fatalf("iteration %d: no llm response line", iter)
				}
				_, hasEstimate := resp["est_request_tokens"]
				switch {
				case tt.responseEstimate[iter] && !hasEstimate:
					t.Errorf("iteration %d: llm response line lacks est_request_tokens", iter)
				case tt.responseEstimate[iter]:
					if got := intAttr(t, resp, "est_request_tokens"); got != want.estimate.total() {
						t.Errorf("iteration %d: response est_request_tokens = %d, want %d", iter, got, want.estimate.total())
					}
				case hasEstimate:
					t.Errorf("iteration %d: response est_request_tokens = %v, want none for a response to a different request",
						iter, resp["est_request_tokens"])
				}
			}
		})
	}
}
