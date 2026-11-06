package agent

import (
	"bytes"
	"context"
	"encoding/json"
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

// TestRun_LLMResponseLineCarriesProvenance pins the INFO "llm response"
// line as the durable record of each call's stop reason and tool-call
// provenance: IterationRecord is not archived, so this line is what a
// later count reads.
func TestRun_LLMResponseLineCarriesProvenance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		lightweight bool
		responses   []*llm.ChatResponse
		want        []map[string]any
		wantAbsent  []string
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
			var calls int
			loop.llm = accountingTestClient{call: func(context.Context, string, []map[string]any) (*llm.ChatResponse, error) {
				if calls >= len(tt.responses) {
					return nil, fmt.Errorf("unexpected model call %d", calls+1)
				}
				resp := *tt.responses[calls]
				calls++
				return &resp, nil
			}}
			logs := &lockedLogBuffer{}
			ctx := logging.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(logs, nil)))
			if _, err := loop.Run(ctx, &Request{
				ConversationID: "provenance-log",
				Model:          "claude-primary",
				SkipContext:    tt.lightweight,
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
