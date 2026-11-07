package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
)

// lockedLogBuffer serializes handler writes so the log can be read
// after a run without racing a late writer.
type lockedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// records returns every JSON log record whose msg is msg.
func (b *lockedLogBuffer) records(t *testing.T, msg string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(b.buf.Bytes()))
	for dec.More() {
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("decode log record: %v", err)
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

// errTestTimeout is a model call that timed out, as isTimeout reads it.
var errTestTimeout = errors.New("request timeout")

// TestRun_LLMResponseLineCarriesProvenance pins the INFO "llm response"
// line as the durable record of each call's stop reason and tool-call
// provenance: IterationRecord is not archived, so this line is what a
// later count reads.
func TestRun_LLMResponseLineCarriesProvenance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		lightweight  bool
		systemPrompt string
		// recoveryModel enables the timeout downshift.
		recoveryModel string
		responses     []*llm.ChatResponse
		// errs fails the model call at that index instead of answering.
		errs map[int]error
		// pullAfter, when nonzero, delivers one user message on the
		// first input poll after that many model calls.
		pullAfter int
		// maxIterations, when nonzero, caps the run's iterations.
		maxIterations int
		// estimated lists the response lines that must carry the
		// estimate their iteration's llm call line logged.
		estimated  []int
		want       []map[string]any
		wantAbsent []string
		// wantFinish, when set, is the run's finish reason.
		wantFinish string
	}{
		{
			name: "full loop",
			responses: []*llm.ChatResponse{
				func() *llm.ChatResponse {
					resp := accountingToolResponse()
					resp.StopReason = "stop"
					resp.ToolCallSource = llm.ToolCallSourceTextFallback
					resp.TextToolAction = llm.TextToolActionRecovered
					return resp
				}(),
				{
					Model: "claude-primary", StopReason: "end_turn",
					Message: llm.Message{Role: "assistant", Content: "Inspected."},
				},
			},
			want: []map[string]any{
				{"stop_reason": "stop", "tool_call_source": "text_fallback", "text_tool_action": "recovered", "profile": "anthropic_native"},
				{"stop_reason": "end_turn", "tool_call_source": "", "text_tool_action": "", "profile": "anthropic_native"},
			},
		},
		{
			// A caller-supplied prompt is used verbatim, so no
			// interaction profile rendered it.
			name:         "custom system prompt",
			systemPrompt: "You are a test loop.",
			responses: []*llm.ChatResponse{{
				Model: "claude-primary", StopReason: "end_turn",
				Message: llm.Message{Role: "assistant", Content: "Done."},
			}},
			want: []map[string]any{{"stop_reason": "end_turn", "profile": ""}},
		},
		{
			// The recovery model answers buildRecoveryPrompt, which no
			// interaction profile rendered.
			name:          "timeout recovery model",
			recoveryModel: "claude-recovery",
			responses: []*llm.ChatResponse{
				accountingToolResponse(),
				nil, nil, nil,
				{
					Model: "claude-recovery", StopReason: "end_turn",
					Message: llm.Message{Role: "assistant", Content: "Inspected before the timeout."},
				},
			},
			errs: map[int]error{1: errTestTimeout, 2: errTestTimeout, 3: errTestTimeout},
			want: []map[string]any{
				{"model": "claude-primary", "profile": "anthropic_native"},
				{"model": "claude-recovery", "stop_reason": "end_turn", "profile": ""},
			},
			wantFinish: "timeout_recovery",
		},
		{
			// When the recovery call itself fails, the reply is a
			// static text attributed to the recovery model; no prompt
			// produced it.
			name:          "timeout recovery model also fails",
			recoveryModel: "claude-recovery",
			responses: []*llm.ChatResponse{
				accountingToolResponse(),
				nil, nil, nil, nil,
			},
			errs: map[int]error{1: errTestTimeout, 2: errTestTimeout, 3: errTestTimeout, 4: errors.New("recovery unavailable")},
			want: []map[string]any{
				{"model": "claude-primary", "profile": "anthropic_native"},
				{"model": "claude-recovery", "stop_reason": "", "profile": ""},
			},
			wantFinish: "timeout_recovery",
		},
		{
			// Input arriving as the recovered reply closes holds the
			// turn open, and the next call answers a prompt rendered
			// afresh for the recovery model the engine switched to,
			// whose profile differs from the primary's. Only the
			// recovered reply goes without a profile.
			name:          "timeout recovery then input at closure",
			recoveryModel: "qwen-recovery",
			responses: []*llm.ChatResponse{
				accountingToolResponse(),
				nil, nil, nil,
				{
					Model: "qwen-recovery", StopReason: "end_turn",
					Message: llm.Message{Role: "assistant", Content: "Inspected before the timeout."},
				},
				{
					Model: "qwen-recovery", StopReason: "end_turn",
					Message: llm.Message{Role: "assistant", Content: "Answered the follow-up."},
				},
			},
			errs:      map[int]error{1: errTestTimeout, 2: errTestTimeout, 3: errTestTimeout},
			pullAfter: 5,
			want: []map[string]any{
				{"model": "claude-primary", "profile": "anthropic_native"},
				{"model": "qwen-recovery", "stop_reason": "end_turn", "profile": ""},
				{"model": "qwen-recovery", "stop_reason": "end_turn", "profile": "local_raw_text_tools"},
			},
			// The follow-up answers its own iteration's request again,
			// though recovery replaced the one before it.
			estimated: []int{0, 2},
		},
		{
			// A recovery reply can carry tool calls, as when a runner
			// recovers them from its text. Once they run out the
			// iterations, the force-text call reports its response
			// without starting an iteration, and it answers the prompt
			// the primary's profile rendered.
			name:          "timeout recovery with tool calls then force text",
			recoveryModel: "claude-recovery",
			maxIterations: 2,
			responses: []*llm.ChatResponse{
				accountingToolResponse(),
				nil, nil, nil,
				func() *llm.ChatResponse {
					resp := accountingToolResponse()
					resp.Model = "claude-recovery"
					resp.Message.ToolCalls[0].ID = "recovery-call"
					return resp
				}(),
				{
					Model: "claude-recovery", StopReason: "end_turn",
					Message: llm.Message{Role: "assistant", Content: "Forced text."},
				},
			},
			errs: map[int]error{1: errTestTimeout, 2: errTestTimeout, 3: errTestTimeout},
			want: []map[string]any{
				{"model": "claude-primary", "profile": "anthropic_native"},
				{"model": "claude-recovery", "profile": ""},
				{"model": "claude-recovery", "stop_reason": "end_turn", "profile": "anthropic_native"},
			},
		},
		{
			// With no recovery model the reply is synthesized; no
			// prompt at all produced it.
			name: "timeout static fallback",
			responses: []*llm.ChatResponse{
				accountingToolResponse(),
				nil, nil, nil,
			},
			errs: map[int]error{1: errTestTimeout, 2: errTestTimeout, 3: errTestTimeout},
			want: []map[string]any{
				{"model": "claude-primary", "profile": "anthropic_native"},
				{"stop_reason": "", "profile": ""},
			},
			wantFinish: "timeout_recovery",
		},
		{
			name:        "lightweight",
			lightweight: true,
			responses: []*llm.ChatResponse{{
				Model: "claude-primary", StopReason: "max_tokens",
				Message: llm.Message{Role: "assistant", Content: "A short title"},
			}},
			want: []map[string]any{{"stop_reason": "max_tokens"}},
			// The lite path offers no tools, so it has no provenance to
			// report and no profile-rendered prompt.
			wantAbsent: []string{"tool_call_source", "text_tool_action", "profile"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			loop, _, _ := newAccountingTestLoop(t)
			loop.recoveryModel = tt.recoveryModel
			var calls int
			loop.llm = accountingTestClient{call: func(context.Context, string, []map[string]any) (*llm.ChatResponse, error) {
				if calls >= len(tt.responses) {
					return nil, fmt.Errorf("unexpected model call %d", calls+1)
				}
				n := calls
				calls++
				if err := tt.errs[n]; err != nil {
					return nil, err
				}
				resp := *tt.responses[n]
				return &resp, nil
			}}
			var pullInput func(context.Context) []llm.Message
			if tt.pullAfter > 0 {
				delivered := false
				pullInput = func(context.Context) []llm.Message {
					if delivered || calls < tt.pullAfter {
						return nil
					}
					delivered = true
					return []llm.Message{{Role: "user", Content: "And the tests?"}}
				}
			}
			logs := &lockedLogBuffer{}
			ctx := logging.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(logs, nil)))
			resp, err := loop.Run(ctx, &Request{
				ConversationID: "provenance-log",
				Model:          "claude-primary",
				SkipContext:    tt.lightweight,
				SystemPrompt:   tt.systemPrompt,
				Messages:       []Message{{Role: "user", Content: "Inspect the source."}},
				PullInput:      pullInput,
				MaxIterations:  tt.maxIterations,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantFinish != "" && resp.FinishReason != tt.wantFinish {
				t.Errorf("finish reason = %q, want %q", resp.FinishReason, tt.wantFinish)
			}

			lines := logs.records(t, "llm response")
			callLines := logs.records(t, "llm call")
			for _, idx := range tt.estimated {
				if idx >= len(lines) {
					t.Fatalf("no response line %d to check its estimate", idx)
				}
				var want any
				for _, call := range callLines {
					if call["iteration"] == lines[idx]["iteration"] {
						want = call["est_request_tokens"]
					}
				}
				if got := lines[idx]["est_request_tokens"]; got == nil || got != want {
					t.Errorf("line %d est_request_tokens = %v, want its llm call line's %v", idx, got, want)
				}
			}
			if len(lines) != len(tt.want) {
				t.Fatalf("llm response lines = %d, want %d: %v", len(lines), len(tt.want), lines)
			}
			for i, want := range tt.want {
				for key, value := range want {
					got, ok := lines[i][key]
					if !ok {
						t.Errorf("line %d missing %q: %v", i, key, lines[i])
						continue
					}
					if got != value {
						t.Errorf("line %d %s = %v, want %v", i, key, got, value)
					}
				}
				for _, key := range tt.wantAbsent {
					if _, ok := lines[i][key]; ok {
						t.Errorf("line %d carries %q: %v", i, key, lines[i])
					}
				}
			}
		})
	}
}
