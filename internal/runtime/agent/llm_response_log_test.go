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
		errs       map[int]error
		want       []map[string]any
		wantAbsent []string
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
			logs := &lockedLogBuffer{}
			ctx := logging.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(logs, nil)))
			if _, err := loop.Run(ctx, &Request{
				ConversationID: "provenance-log",
				Model:          "claude-primary",
				SkipContext:    tt.lightweight,
				SystemPrompt:   tt.systemPrompt,
				Messages:       []Message{{Role: "user", Content: "Inspect the source."}},
			}, nil); err != nil {
				t.Fatal(err)
			}

			lines := logs.records(t, "llm response")
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
