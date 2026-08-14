package iterate_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/fleet/providers"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/runtime/iterate"
)

type lmStudioFollowUpRequest struct {
	Stream   bool                      `json:"stream"`
	Messages []lmStudioFollowUpMessage `json:"messages"`
}

type lmStudioFollowUpMessage struct {
	Role       string                     `json:"role"`
	ToolCallID string                     `json:"tool_call_id"`
	ToolCalls  []lmStudioFollowUpToolCall `json:"tool_calls"`
}

type lmStudioFollowUpToolCall struct {
	ID string `json:"id"`
}

func TestLMStudioMissingToolCallIDRoundTrip(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req lmStudioFollowUpRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !req.Stream {
			t.Fatal("expected streaming request")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeChunk := func(chunk any) {
			data, err := json.Marshal(chunk)
			if err != nil {
				t.Fatalf("marshal chunk: %v", err)
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		switch requestCount.Add(1) {
		case 1:
			// Qwen tool parsing can produce a valid call without the id that
			// LM Studio later requires when the call returns in history.
			writeChunk(map[string]any{
				"model": "qwen/qwen3-coder-next",
				"choices": []any{map[string]any{
					"index": 0,
					"delta": map[string]any{
						"role": "assistant",
						"tool_calls": []any{map[string]any{
							"index": 0,
							"type":  "function",
							"function": map[string]any{
								"name":      "echo",
								"arguments": `{"text":"probe"}`,
							},
						}},
					},
				}},
				"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 4},
			})
		case 2:
			if len(req.Messages) != 4 {
				t.Fatalf("follow-up messages = %d, want 4", len(req.Messages))
			}
			assistant := req.Messages[2]
			toolResult := req.Messages[3]
			if len(assistant.ToolCalls) != 1 {
				t.Fatalf("assistant tool calls = %d, want 1", len(assistant.ToolCalls))
			}
			callID := assistant.ToolCalls[0].ID
			if callID == "" {
				http.Error(w, `{"error":"Invalid 'messages' in payload. Please check the structure of your 'messages' and try again."}`, http.StatusBadRequest)
				return
			}
			if toolResult.ToolCallID != callID {
				t.Fatalf("tool result ID = %q, want assistant call ID %q", toolResult.ToolCallID, callID)
			}
			writeChunk(map[string]any{
				"model": "qwen/qwen3-coder-next",
				"choices": []any{map[string]any{
					"index": 0,
					"delta": map[string]any{"role": "assistant", "content": "done"},
				}},
				"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 1},
			})
		default:
			t.Fatalf("unexpected request %d", requestCount.Load())
		}

		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	toolDefs := []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name": "echo",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{"type": "string"},
				},
			},
		},
	}}
	client := providers.NewLMStudioClient(srv.URL, "", nil)
	result, err := (&iterate.Engine{}).Run(context.Background(), iterate.Config{
		MaxIterations: 3,
		Model:         "qwen/qwen3-coder-next",
		LLM:           client,
		Stream:        func(llm.StreamEvent) {},
		ToolDefs:      func(int) []map[string]any { return toolDefs },
		Executor: &iterate.DirectExecutor{Exec: func(context.Context, string, string) (string, error) {
			return `{"text":"probe"}`, nil
		}},
	}, []llm.Message{
		{Role: "system", Content: "Use tools when requested."},
		{Role: "user", Content: "Echo probe."},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Content != "done" {
		t.Fatalf("result.Content = %q, want done", result.Content)
	}
	if got := requestCount.Load(); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
}
