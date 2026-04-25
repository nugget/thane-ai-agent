package agent

import (
	"strings"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
)

// progressCall records a single invocation of the progressFn.
type progressCall struct {
	kind string
	data map[string]any
}

// collectProgress returns a progressFn and a function to retrieve
// the collected calls. Thread-safe.
func collectProgress() (func(string, map[string]any), func() []progressCall) {
	var mu sync.Mutex
	var calls []progressCall
	return func(kind string, data map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, progressCall{kind: kind, data: data})
		}, func() []progressCall {
			mu.Lock()
			defer mu.Unlock()
			cp := make([]progressCall, len(calls))
			copy(cp, calls)
			return cp
		}
}

func TestBuildProgressStream_NilProgressFn(t *testing.T) {
	t.Parallel()
	if cb := BuildProgressStream(nil); cb != nil {
		t.Fatal("expected nil callback for nil progressFn")
	}
}

func TestBuildProgressStream_LLMStart(t *testing.T) {
	t.Parallel()
	fn, get := collectProgress()
	cb := BuildProgressStream(fn)

	cb(StreamEvent{
		Kind: KindLLMStart,
		Response: &llm.ChatResponse{
			Model: "claude-3-opus",
		},
		Data: map[string]any{
			"est_tokens": 5000,
			"complexity": "high",
		},
	})

	calls := get()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	c := calls[0]
	if c.kind != events.KindLoopLLMStart {
		t.Errorf("kind = %q, want %q", c.kind, events.KindLoopLLMStart)
	}
	if c.data["model"] != "claude-3-opus" {
		t.Errorf("model = %v, want claude-3-opus", c.data["model"])
	}
	if c.data["est_tokens"] != 5000 {
		t.Errorf("est_tokens = %v, want 5000", c.data["est_tokens"])
	}
	if c.data["complexity"] != "high" {
		t.Errorf("complexity = %v, want high", c.data["complexity"])
	}
}

func TestBuildProgressStream_LLMStart_NilResponse(t *testing.T) {
	t.Parallel()
	fn, get := collectProgress()
	cb := BuildProgressStream(fn)

	cb(StreamEvent{Kind: KindLLMStart, Response: nil})

	if calls := get(); len(calls) != 0 {
		t.Fatalf("expected 0 calls for nil response, got %d", len(calls))
	}
}

func TestBuildProgressStream_ToolCallStart(t *testing.T) {
	t.Parallel()
	fn, get := collectProgress()
	cb := BuildProgressStream(fn)

	cb(StreamEvent{
		Kind: KindToolCallStart,
		ToolCall: &llm.ToolCall{
			Function: struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}{
				Name:      "shell_exec",
				Arguments: map[string]any{"command": "ls -la"},
			},
		},
	})

	calls := get()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	c := calls[0]
	if c.kind != events.KindLoopToolStart {
		t.Errorf("kind = %q, want %q", c.kind, events.KindLoopToolStart)
	}
	if c.data["tool"] != "shell_exec" {
		t.Errorf("tool = %v, want shell_exec", c.data["tool"])
	}
	args, ok := c.data["args"].(map[string]any)
	if !ok {
		t.Fatal("args not passed through")
	}
	if args["command"] != "ls -la" {
		t.Errorf("args.command = %v, want ls -la", args["command"])
	}
}

func TestBuildProgressStream_ToolCallStart_NoArgs(t *testing.T) {
	t.Parallel()
	fn, get := collectProgress()
	cb := BuildProgressStream(fn)

	cb(StreamEvent{
		Kind: KindToolCallStart,
		ToolCall: &llm.ToolCall{
			Function: struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}{
				Name: "get_time",
			},
		},
	})

	calls := get()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if _, ok := calls[0].data["args"]; ok {
		t.Error("args should be omitted when empty")
	}
}

func TestBuildProgressStream_ToolCallDone(t *testing.T) {
	t.Parallel()
	fn, get := collectProgress()
	cb := BuildProgressStream(fn)

	cb(StreamEvent{
		Kind:       KindToolCallDone,
		ToolName:   "web_search",
		ToolResult: "found 3 results",
	})

	calls := get()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	c := calls[0]
	if c.kind != events.KindLoopToolDone {
		t.Errorf("kind = %q, want %q", c.kind, events.KindLoopToolDone)
	}
	if c.data["tool"] != "web_search" {
		t.Errorf("tool = %v, want web_search", c.data["tool"])
	}
	if c.data["result"] != "found 3 results" {
		t.Errorf("result = %v, want 'found 3 results'", c.data["result"])
	}
	if _, ok := c.data["error"]; ok {
		t.Error("error field should be omitted on success")
	}
}

func TestBuildProgressStream_ToolCallDone_WithError(t *testing.T) {
	t.Parallel()
	fn, get := collectProgress()
	cb := BuildProgressStream(fn)

	cb(StreamEvent{
		Kind:      KindToolCallDone,
		ToolName:  "shell_exec",
		ToolError: "permission denied",
	})

	calls := get()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].data["error"] != "permission denied" {
		t.Errorf("error = %v, want 'permission denied'", calls[0].data["error"])
	}
}

func TestBuildProgressStream_ToolCallDone_Truncation(t *testing.T) {
	t.Parallel()
	fn, get := collectProgress()
	cb := BuildProgressStream(fn)

	longResult := strings.Repeat("x", maxProgressToolResultLen+500)
	cb(StreamEvent{
		Kind:       KindToolCallDone,
		ToolName:   "read_file",
		ToolResult: longResult,
	})

	calls := get()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	result := calls[0].data["result"].(string)
	if len(result) > maxProgressToolResultLen+len("…") {
		t.Errorf("result not truncated: len=%d, want <= %d", len(result), maxProgressToolResultLen+len("…"))
	}
	if !strings.HasSuffix(result, "…") {
		t.Error("truncated result should end with ellipsis")
	}
}

func TestBuildProgressStream_LLMResponse(t *testing.T) {
	t.Parallel()
	fn, get := collectProgress()
	cb := BuildProgressStream(fn)

	cb(StreamEvent{
		Kind: KindLLMResponse,
		Response: &llm.ChatResponse{
			Model:        "claude-3-haiku",
			InputTokens:  1200,
			OutputTokens: 300,
		},
	})

	calls := get()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	c := calls[0]
	if c.kind != events.KindLoopLLMResponse {
		t.Errorf("kind = %q, want %q", c.kind, events.KindLoopLLMResponse)
	}
	if c.data["model"] != "claude-3-haiku" {
		t.Errorf("model = %v, want claude-3-haiku", c.data["model"])
	}
	if c.data["input_tokens"] != 1200 {
		t.Errorf("input_tokens = %v, want 1200", c.data["input_tokens"])
	}
	if c.data["output_tokens"] != 300 {
		t.Errorf("output_tokens = %v, want 300", c.data["output_tokens"])
	}
}

func TestBuildProgressStream_LLMResponse_NilResponse(t *testing.T) {
	t.Parallel()
	fn, get := collectProgress()
	cb := BuildProgressStream(fn)

	cb(StreamEvent{Kind: KindLLMResponse, Response: nil})

	if calls := get(); len(calls) != 0 {
		t.Fatalf("expected 0 calls for nil response, got %d", len(calls))
	}
}

func TestBuildProgressStream_UnknownKind(t *testing.T) {
	t.Parallel()
	fn, get := collectProgress()
	cb := BuildProgressStream(fn)

	cb(StreamEvent{Kind: 999})

	if calls := get(); len(calls) != 0 {
		t.Fatalf("expected 0 calls for unknown kind, got %d", len(calls))
	}
}
