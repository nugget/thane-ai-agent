package iterate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/fleet/providers"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/runtime/iterate"
)

// provenanceCall is one structured call the fixture server returns.
// An empty id is the LM Studio Qwen parser's omission.
type provenanceCall struct {
	id, name, args string
}

// provenanceFixture is the first assistant reply an OpenAI-compatible
// server sends. A reply that yields tool calls is followed by a plain
// "done" on the second request.
type provenanceFixture struct {
	content      string
	calls        []provenanceCall
	finishReason string
}

func (f provenanceFixture) message() map[string]any {
	msg := map[string]any{"role": "assistant", "content": f.content}
	if len(f.calls) > 0 {
		calls := make([]any, 0, len(f.calls))
		for i, c := range f.calls {
			call := map[string]any{
				"index":    i,
				"type":     "function",
				"function": map[string]any{"name": c.name, "arguments": c.args},
			}
			if c.id != "" {
				call["id"] = c.id
			}
			calls = append(calls, call)
		}
		msg["tool_calls"] = calls
	}
	return msg
}

// newProvenanceServer answers the first request with the fixture and the
// second with "done", in whichever shape the request asked for.
func newProvenanceServer(t *testing.T, first provenanceFixture) *httptest.Server {
	t.Helper()
	var requests atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		reply := provenanceFixture{content: "done", finishReason: "stop"}
		switch n := requests.Add(1); n {
		case 1:
			reply = first
		case 2:
		default:
			t.Errorf("unexpected request %d", n)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		usage := map[string]any{"prompt_tokens": 12, "completion_tokens": 4}
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"model": "synthetic-local",
				"choices": []any{map[string]any{
					"index":         0,
					"message":       reply.message(),
					"finish_reason": reply.finishReason,
				}},
				"usage": usage,
			}); err != nil {
				t.Errorf("encode response: %v", err)
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range []map[string]any{
			{"model": "synthetic-local", "choices": []any{map[string]any{"index": 0, "delta": reply.message()}}},
			{"model": "synthetic-local", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": reply.finishReason}}, "usage": usage},
		} {
			data, err := json.Marshal(chunk)
			if err != nil {
				t.Errorf("marshal chunk: %v", err)
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

// TestToolCallProvenanceOnIterations runs every fallback shape through
// the real openai_compat client, streamed and not, and reads the
// provenance off Result.Iterations, where the agent loop finds it.
func TestToolCallProvenanceOnIterations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		first       provenanceFixture
		wantSource  llm.ToolCallSource
		wantAction  llm.TextToolAction
		wantStop    string
		wantExec    []string
		wantContent string
		// nonStreamErr is the error the non-streaming path returns where
		// the two paths diverge: it judges emptiness after the fallback
		// has cleared the content, the stream judges it before.
		nonStreamErr error
	}{
		{
			name: "native call with id",
			first: provenanceFixture{
				calls:        []provenanceCall{{id: "call_runner_1", name: "echo", args: `{"text":"probe"}`}},
				finishReason: "tool_calls",
			},
			wantSource: llm.ToolCallSourceNative, wantAction: llm.TextToolActionNone, wantStop: "tool_calls",
			wantExec: []string{"echo"}, wantContent: "done",
		},
		{
			name: "native call without id reads native",
			first: provenanceFixture{
				calls:        []provenanceCall{{name: "echo", args: `{"text":"probe"}`}},
				finishReason: "tool_calls",
			},
			wantSource: llm.ToolCallSourceNative, wantAction: llm.TextToolActionNone, wantStop: "tool_calls",
			wantExec: []string{"echo"}, wantContent: "done",
		},
		{
			name:       "whole-text json",
			first:      provenanceFixture{content: `{"name":"echo","arguments":{"text":"probe"}}`, finishReason: "stop"},
			wantSource: llm.ToolCallSourceTextFallback, wantAction: llm.TextToolActionRecovered, wantStop: "stop",
			wantExec: []string{"echo"}, wantContent: "done",
		},
		{
			name:       "fenced",
			first:      provenanceFixture{content: "```json\n{\"name\":\"echo\",\"arguments\":{\"text\":\"probe\"}}\n```", finishReason: "stop"},
			wantSource: llm.ToolCallSourceTextFallback, wantAction: llm.TextToolActionRecovered, wantStop: "stop",
			wantExec: []string{"echo"}, wantContent: "done",
		},
		{
			name:       "tagged",
			first:      provenanceFixture{content: "<tool_call>\n{\"name\":\"echo\",\"arguments\":{\"text\":\"probe\"}}\n</tool_call>", finishReason: "stop"},
			wantSource: llm.ToolCallSourceTextFallback, wantAction: llm.TextToolActionRecovered, wantStop: "stop",
			wantExec: []string{"echo"}, wantContent: "done",
		},
		{
			name:       "unoffered name",
			first:      provenanceFixture{content: `{"name":"request_capability","arguments":{"tag":"forge"}}`, finishReason: "stop"},
			wantSource: llm.ToolCallSourceTextFallback, wantAction: llm.TextToolActionRepairRecoveredUnoffered, wantStop: "stop",
			wantExec: []string{"request_capability"}, wantContent: "done",
		},
		{
			name:       "stringified arguments are suppressed",
			first:      provenanceFixture{content: `{"name":"echo","arguments":"{\"text\":\"probe\"}"}`, finishReason: "stop"},
			wantSource: "", wantAction: llm.TextToolActionSuppressed, wantStop: "stop",
			nonStreamErr: llm.ErrEmptyCompletion,
		},
		{
			name:       "fenced call after prose is stripped",
			first:      provenanceFixture{content: "Echoing the probe.\n```tool\n{\"name\":\"echo\",\"arguments\":{\"text\":\"probe\"}}\n```", finishReason: "stop"},
			wantSource: "", wantAction: llm.TextToolActionStrippedTrailing, wantStop: "stop",
			wantContent: "Echoing the probe.",
		},
		{
			name:       "plain text",
			first:      provenanceFixture{content: "Nothing to echo.", finishReason: "stop"},
			wantSource: "", wantAction: llm.TextToolActionNone, wantStop: "stop",
			wantContent: "Nothing to echo.",
		},
	}
	for _, tt := range tests {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tt.name, stream), func(t *testing.T) {
				t.Parallel()
				srv := newProvenanceServer(t, tt.first)
				defer srv.Close()

				var (
					mu       sync.Mutex
					executed []string
				)
				cfg := iterate.Config{
					MaxIterations: 3,
					Model:         "synthetic-local",
					LLM:           providers.NewOpenAICompatClient(srv.URL, "", "vllm", "", nil, 0),
					ToolDefs: func(int) []map[string]any {
						return []map[string]any{{
							"type": "function",
							"function": map[string]any{
								"name":       "echo",
								"parameters": map[string]any{"type": "object"},
							},
						}}
					},
					Executor: &iterate.DirectExecutor{Exec: func(_ context.Context, name, _ string) (string, error) {
						mu.Lock()
						defer mu.Unlock()
						executed = append(executed, name)
						return "ok", nil
					}},
				}
				if stream {
					cfg.Stream = func(llm.StreamEvent) {}
				}
				result, err := (&iterate.Engine{}).Run(context.Background(), cfg, []llm.Message{
					{Role: "system", Content: "Use tools when asked."},
					{Role: "user", Content: "Echo the probe."},
				})
				if !stream && tt.nonStreamErr != nil {
					if !errors.Is(err, tt.nonStreamErr) {
						t.Fatalf("Run() error = %v, want %v", err, tt.nonStreamErr)
					}
					return
				}
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				if len(result.Iterations) == 0 {
					t.Fatal("no iterations recorded")
				}
				first := result.Iterations[0]
				if first.ToolCallSource != tt.wantSource {
					t.Errorf("Iterations[0].ToolCallSource = %q, want %q", first.ToolCallSource, tt.wantSource)
				}
				if first.TextToolAction != tt.wantAction {
					t.Errorf("Iterations[0].TextToolAction = %q, want %q", first.TextToolAction, tt.wantAction)
				}
				if first.StopReason != tt.wantStop {
					t.Errorf("Iterations[0].StopReason = %q, want %q", first.StopReason, tt.wantStop)
				}
				if len(tt.wantExec) > 0 {
					if len(first.ToolCallIDs) != len(tt.wantExec) {
						t.Fatalf("Iterations[0].ToolCallIDs = %v, want %d", first.ToolCallIDs, len(tt.wantExec))
					}
					if len(result.Iterations) != 2 {
						t.Fatalf("iterations = %d, want 2", len(result.Iterations))
					}
					// The closing text reply made no calls, so it reports
					// no source and no fallback change.
					last := result.Iterations[1]
					if last.ToolCallSource != "" || last.TextToolAction != llm.TextToolActionNone {
						t.Errorf("Iterations[1] provenance = %q/%q, want empty/none", last.ToolCallSource, last.TextToolAction)
					}
				}
				mu.Lock()
				gotExec := strings.Join(executed, ",")
				mu.Unlock()
				if gotExec != strings.Join(tt.wantExec, ",") {
					t.Errorf("executed = %q, want %q", gotExec, strings.Join(tt.wantExec, ","))
				}
				if result.Content != tt.wantContent {
					t.Errorf("Content = %q, want %q", result.Content, tt.wantContent)
				}
			})
		}
	}
}
