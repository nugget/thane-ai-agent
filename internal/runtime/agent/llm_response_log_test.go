package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/runtime/iterate"
	"github.com/nugget/thane-ai-agent/internal/tools"
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
// later count reads. It also pins the run's finish reason, which names
// timeout_recovery only when timeout recovery supplied the reply the
// run ended on. It pins the model each call was sent to, which timeout
// recovery changes for its one reply and no other, and the model the
// Response credits.
func TestRun_LLMResponseLineCarriesProvenance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		lightweight  bool
		systemPrompt string
		// recoveryModel enables the timeout downshift.
		recoveryModel string
		// pin, when set, holds the conversation to that model, and the
		// request names no model of its own, so failover to the loop's
		// default, claude-primary, stays in play.
		pin       string
		responses []*llm.ChatResponse
		// errs fails the model call at that index instead of answering.
		errs map[int]error
		// wantDispatched is the model each call was sent to, in order.
		wantDispatched []string
		// wantModel is the model the Response credits, and the live
		// request record with it.
		wantModel string
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
		// wantFinish is the run's finish reason.
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
			wantDispatched: []string{"claude-primary", "claude-primary"},
			wantModel:      "claude-primary",
			wantFinish:     "stop",
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
			want:           []map[string]any{{"stop_reason": "end_turn", "profile": ""}},
			wantDispatched: []string{"claude-primary"},
			wantModel:      "claude-primary",
			wantFinish:     "stop",
		},
		{
			// A failover moves the run itself: the pinned model fails,
			// the loop's default answers in its place, and the next
			// iteration's prompt is rendered afresh for the default,
			// whose profile differs from the pinned model's. The
			// failover's own reply answered the prompt rendered for
			// the pinned model.
			name: "failover to a model with another profile",
			pin:  "qwen-pinned",
			responses: []*llm.ChatResponse{
				nil,
				accountingToolResponse(),
				{
					Model: "claude-primary", StopReason: "end_turn",
					Message: llm.Message{Role: "assistant", Content: "Inspected after failover."},
				},
			},
			errs: map[int]error{0: errors.New("provider unavailable")},
			want: []map[string]any{
				{"model": "claude-primary", "profile": "local_raw_text_tools"},
				{"model": "claude-primary", "stop_reason": "end_turn", "profile": "anthropic_native"},
			},
			wantDispatched: []string{"qwen-pinned", "claude-primary", "claude-primary"},
			wantModel:      "claude-primary",
			wantFinish:     "stop",
		},
		{
			// The recovery model answers buildRecoveryPrompt, which no
			// interaction profile rendered. Its reply ends the turn, so
			// the Response credits it.
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
			wantDispatched: []string{"claude-primary", "claude-primary", "claude-primary", "claude-primary", "claude-recovery"},
			wantModel:      "claude-recovery",
			wantFinish:     "timeout_recovery",
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
			wantDispatched: []string{"claude-primary", "claude-primary", "claude-primary", "claude-primary", "claude-recovery"},
			wantModel:      "claude-recovery",
			wantFinish:     "timeout_recovery",
		},
		{
			// Input arriving as the recovered reply closes holds the
			// turn open. Recovery answered that one reply only, so the
			// follow-up goes back to the run's own model, not to the
			// recovery model, whose profile differs, and answers a
			// prompt rendered for the run's model. Only the recovered
			// reply goes without a profile. The run's model wrote the
			// reply the turn ends on, so the run finishes as stop and
			// the Response credits that model.
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
					Model: "claude-primary", StopReason: "end_turn",
					Message: llm.Message{Role: "assistant", Content: "Answered the follow-up."},
				},
			},
			errs:      map[int]error{1: errTestTimeout, 2: errTestTimeout, 3: errTestTimeout},
			pullAfter: 5,
			want: []map[string]any{
				{"model": "claude-primary", "profile": "anthropic_native"},
				{"model": "qwen-recovery", "stop_reason": "end_turn", "profile": ""},
				{"model": "claude-primary", "stop_reason": "end_turn", "profile": "anthropic_native"},
			},
			// The follow-up answers its own iteration's request again,
			// though recovery replaced the one before it.
			estimated:      []int{0, 2},
			wantDispatched: []string{"claude-primary", "claude-primary", "claude-primary", "claude-primary", "qwen-recovery", "claude-primary"},
			wantModel:      "claude-primary",
			wantFinish:     "stop",
		},
		{
			// The canned text that stands in for a failed recovery
			// call leaves the run on its own model too.
			name:          "timeout recovery fails then input at closure",
			recoveryModel: "qwen-recovery",
			responses: []*llm.ChatResponse{
				accountingToolResponse(),
				nil, nil, nil, nil,
				{
					Model: "claude-primary", StopReason: "end_turn",
					Message: llm.Message{Role: "assistant", Content: "Answered the follow-up."},
				},
			},
			errs:      map[int]error{1: errTestTimeout, 2: errTestTimeout, 3: errTestTimeout, 4: errors.New("recovery unavailable")},
			pullAfter: 5,
			want: []map[string]any{
				{"model": "claude-primary", "profile": "anthropic_native"},
				{"model": "qwen-recovery", "stop_reason": "", "profile": ""},
				{"model": "claude-primary", "stop_reason": "end_turn", "profile": "anthropic_native"},
			},
			wantDispatched: []string{"claude-primary", "claude-primary", "claude-primary", "claude-primary", "qwen-recovery", "claude-primary"},
			wantModel:      "claude-primary",
			wantFinish:     "stop",
		},
		{
			// A recovery reply carries no tool calls, so a force-text
			// call follows one only after input at closure held the
			// turn open and the run's own model ran out the iterations
			// with tool calls. That call reports its response without
			// starting an iteration. It goes to the run's model, which
			// wrote the reply the run ends on, and answers the prompt
			// that model's profile rendered. The run is exhausted, so
			// it finishes with the reason it ran out.
			name:          "timeout recovery then input at closure then force text",
			recoveryModel: "claude-recovery",
			maxIterations: 3,
			responses: []*llm.ChatResponse{
				accountingToolResponse(),
				nil, nil, nil,
				{
					Model: "claude-recovery", StopReason: "end_turn",
					Message: llm.Message{Role: "assistant", Content: "Inspected before the timeout."},
				},
				accountingToolResponse(),
				{
					Model: "claude-primary", StopReason: "end_turn",
					Message: llm.Message{Role: "assistant", Content: "Forced text."},
				},
			},
			errs:      map[int]error{1: errTestTimeout, 2: errTestTimeout, 3: errTestTimeout},
			pullAfter: 5,
			want: []map[string]any{
				{"model": "claude-primary", "profile": "anthropic_native"},
				{"model": "claude-recovery", "profile": ""},
				{"model": "claude-primary", "profile": "anthropic_native"},
				{"model": "claude-primary", "stop_reason": "end_turn", "profile": "anthropic_native"},
			},
			wantDispatched: []string{"claude-primary", "claude-primary", "claude-primary", "claude-primary", "claude-recovery", "claude-primary", "claude-primary"},
			wantModel:      "claude-primary",
			wantFinish:     iterate.ExhaustMaxIterations,
		},
		{
			// When input arrives at closure on the run's last
			// iteration, the turn is held open with no iteration left
			// and no tool result to force text after, so the recovered
			// response is the last one reported but the run ends on no
			// reply. It still finishes with the reason it ran out. The
			// Response credits the model of the last response the run
			// reported, which is the recovery model's.
			name:          "timeout recovery then input at closure on the last iteration",
			recoveryModel: "claude-recovery",
			maxIterations: 2,
			responses: []*llm.ChatResponse{
				accountingToolResponse(),
				nil, nil, nil,
				{
					Model: "claude-recovery", StopReason: "end_turn",
					Message: llm.Message{Role: "assistant", Content: "Inspected before the timeout."},
				},
			},
			errs:      map[int]error{1: errTestTimeout, 2: errTestTimeout, 3: errTestTimeout},
			pullAfter: 5,
			want: []map[string]any{
				{"model": "claude-primary", "profile": "anthropic_native"},
				{"model": "claude-recovery", "profile": ""},
			},
			wantDispatched: []string{"claude-primary", "claude-primary", "claude-primary", "claude-primary", "claude-recovery"},
			wantModel:      "claude-recovery",
			wantFinish:     iterate.ExhaustMaxIterations,
		},
		{
			// With no recovery model the reply is synthesized; no
			// prompt at all produced it, and no model wrote it, so the
			// Response credits the run's own model.
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
			wantDispatched: []string{"claude-primary", "claude-primary", "claude-primary", "claude-primary"},
			wantModel:      "claude-primary",
			wantFinish:     "timeout_recovery",
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
			wantAbsent:     []string{"tool_call_source", "text_tool_action", "profile"},
			wantDispatched: []string{"claude-primary"},
			wantModel:      "claude-primary",
			wantFinish:     "stop",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			loop, _, _ := newAccountingTestLoop(t)
			loop.recoveryModel = tt.recoveryModel
			requestModel := "claude-primary"
			if tt.pin != "" {
				requestModel = ""
				loop.conversationPins.set(tools.ConversationModelPin{
					ConversationID: "provenance-log", Model: tt.pin, PinnedAt: time.Now(),
				})
			}
			var calls int
			var dispatched []string
			loop.llm = accountingTestClient{call: func(_ context.Context, model string, _ []map[string]any) (*llm.ChatResponse, error) {
				dispatched = append(dispatched, model)
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
			var live logging.RequestContent
			loop.liveRequestRecorder = func(_ context.Context, content logging.RequestContent) { live = content }
			logs := &lockedLogBuffer{}
			ctx := logging.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(logs, nil)))
			resp, err := loop.Run(ctx, &Request{
				ConversationID: "provenance-log",
				Model:          requestModel,
				SkipContext:    tt.lightweight,
				SystemPrompt:   tt.systemPrompt,
				Messages:       []Message{{Role: "user", Content: "Inspect the source."}},
				PullInput:      pullInput,
				MaxIterations:  tt.maxIterations,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.FinishReason != tt.wantFinish {
				t.Errorf("finish reason = %q, want %q", resp.FinishReason, tt.wantFinish)
			}
			if !slices.Equal(dispatched, tt.wantDispatched) {
				t.Errorf("dispatched models = %v, want %v", dispatched, tt.wantDispatched)
			}
			if resp.Model != tt.wantModel {
				t.Errorf("response model = %q, want %q", resp.Model, tt.wantModel)
			}
			// The lite path keeps no live request record.
			if !tt.lightweight && live.Model != tt.wantModel {
				t.Errorf("live request model = %q, want %q", live.Model, tt.wantModel)
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
