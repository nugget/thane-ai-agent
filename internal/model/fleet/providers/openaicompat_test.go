package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
)

// TestOpenAICompatStreamErrorIsNotSuccess pins the defect that motivated
// this client. Ollama's native adapter had no error field on its chunk
// type, so a mid-stream failure decoded as an empty chunk and the turn
// was reported as a successful completion with zero tokens — a
// fabricated success the loop and its telemetry both believed. An error
// frame must end the stream as an error, in either shape servers send.
func TestOpenAICompatStreamErrorIsNotSuccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		frame string
		want  string
	}{
		{name: "object error", frame: `{"error":{"message":"CUDA out of memory"}}`, want: "CUDA out of memory"},
		{name: "bare string error", frame: `{"error":"model runner exited"}`, want: "model runner exited"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				// A normal chunk lands first, so the failure arrives
				// mid-stream exactly as it does in production.
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
				_, _ = w.Write([]byte("data: " + tt.frame + "\n\n"))
			}))
			defer srv.Close()

			c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
			resp, err := c.ChatStream(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, func(llm.StreamEvent) {})
			if err == nil {
				t.Fatalf("ChatStream returned success on an error frame: %#v", resp)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to name %q", err, tt.want)
			}
		})
	}
}

// TestOpenAICompatRequestsUsageAndOmitsTTL pins the two request-shape
// facts the generic path depends on: streaming asks for usage (without
// it there are no token counts, and the context-fill gauge has nothing
// to read), and the LM Studio-only ttl field stays off the wire for
// servers that would not recognize it.
func TestOpenAICompatRequestsUsageAndOmitsTTL(t *testing.T) {
	t.Parallel()

	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
	if _, err := c.ChatStream(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, func(llm.StreamEvent) {}); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	opts, ok := body["stream_options"].(map[string]any)
	if !ok || opts["include_usage"] != true {
		t.Errorf("stream_options = %#v, want include_usage true", body["stream_options"])
	}
	if _, present := body["ttl"]; present {
		t.Errorf("ttl present for a non-LM-Studio endpoint: %#v", body["ttl"])
	}
}

// TestOpenAICompatModelInfoContextCeiling pins the reconciliation of the
// two spellings servers use, and that silence reads as "not reported"
// rather than as a zero-size window.
func TestOpenAICompatModelInfoContextCeiling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info OpenAICompatModelInfo
		want int
	}{
		{name: "lmstudio spelling", info: OpenAICompatModelInfo{MaxContextLength: 8192}, want: 8192},
		{name: "vllm spelling", info: OpenAICompatModelInfo{MaxModelLen: 131072}, want: 131072},
		{name: "lmstudio wins when both present", info: OpenAICompatModelInfo{MaxContextLength: 8192, MaxModelLen: 4096}, want: 8192},
		{name: "plain openai server reports neither", info: OpenAICompatModelInfo{ID: "m"}, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.info.ContextCeiling(); got != tt.want {
				t.Errorf("ContextCeiling() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestOpenAICompatEmptyStreamIsNotSuccess covers the gap a frame counter
// leaves open. A role-only opening frame and a usage-only trailer are
// both well-formed and both increment the count, so a stream carrying
// nothing but scaffolding would pass a chunks>0 check and be reported as
// a completed turn with zero tokens — the same fabricated success an
// undetected error frame produces, arriving by a quieter route.
func TestOpenAICompatEmptyStreamIsNotSuccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		frames []string
		wantOK bool
	}{
		{
			name:   "role-only frame then clean close",
			frames: []string{`{"choices":[{"delta":{"role":"assistant"}}]}`, "[DONE]"},
		},
		{
			name: "usage-only trailer, no content",
			frames: []string{
				`{"choices":[{"delta":{"role":"assistant"}}]}`,
				`{"choices":[],"usage":{"prompt_tokens":41,"completion_tokens":0}}`,
				"[DONE]",
			},
		},
		{
			name:   "no frames at all",
			frames: []string{"[DONE]"},
		},
		{
			name:   "real content is a real completion",
			frames: []string{`{"choices":[{"delta":{"content":"hello"}}]}`, "[DONE]"},
			wantOK: true,
		},
		{
			name:   "tool call with no text is a real completion",
			frames: []string{`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"ha_get_state","arguments":"{}"}}]}}]}`, "[DONE]"},
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, f := range tt.frames {
					_, _ = w.Write([]byte("data: " + f + "\n\n"))
				}
			}))
			defer srv.Close()

			c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
			resp, err := c.ChatStream(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, func(llm.StreamEvent) {})
			if tt.wantOK {
				if err != nil {
					t.Fatalf("ChatStream: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("empty stream reported success: content=%q tools=%d tokens=%d/%d",
					resp.Message.Content, len(resp.Message.ToolCalls), resp.InputTokens, resp.OutputTokens)
			}
		})
	}
}

// TestOpenAICompatCapturesFinishReason pins the provider termination
// signal that the iteration layer records. Without it a truncated answer
// ("length") is indistinguishable from a complete one.
func TestOpenAICompatCapturesFinishReason(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"truncated\"}}]}\n\n"))
		// The terminating frame carries finish_reason with an empty delta.
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
	resp, err := c.ChatStream(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, func(llm.StreamEvent) {})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if resp.StopReason != "length" {
		t.Errorf("StopReason = %q, want \"length\"", resp.StopReason)
	}
}

// TestOpenAICompatParallelToolCallsNonStreaming pins the identity of a
// tool call in a non-streaming response. `index` is a streaming-delta
// field for reassembling out-of-order chunks; a non-streaming array
// carries none, so every element decodes with Index 0. Keying on it
// collapsed parallel calls into one accumulator — last id and name won,
// and the arguments concatenated into JSON that failed to parse, so a
// good two-call turn became an error. vLLM emits parallel calls by
// default, which makes this the common case, not the exotic one.
func TestOpenAICompatParallelToolCallsNonStreaming(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-abc123",
			"model": "m",
			"choices": [{"message": {"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "ha_get_state", "arguments": "{\"entity_id\":\"light.office\"}"}},
				{"id": "call_2", "type": "function", "function": {"name": "doc_read", "arguments": "{\"path\":\"kb:a.md\"}"}}
			]}, "finish_reason": "tool_calls"}]
		}`))
	}))
	defer srv.Close()

	c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
	resp, err := c.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Message.ToolCalls) != 2 {
		t.Fatalf("tool calls = %d, want 2: %#v", len(resp.Message.ToolCalls), resp.Message.ToolCalls)
	}
	byName := map[string]llm.ToolCall{}
	for _, tc := range resp.Message.ToolCalls {
		byName[tc.Function.Name] = tc
	}
	if got, ok := byName["ha_get_state"]; !ok || got.ID != "call_1" {
		t.Errorf("ha_get_state = %#v, want id call_1", got)
	}
	if got, ok := byName["doc_read"]; !ok || got.ID != "call_2" {
		t.Errorf("doc_read = %#v, want id call_2", got)
	}
	if resp.StopReason != "tool_calls" {
		t.Errorf("StopReason = %q, want tool_calls", resp.StopReason)
	}
	if resp.UpstreamRequestID != "chatcmpl-abc123" {
		t.Errorf("UpstreamRequestID = %q, want chatcmpl-abc123", resp.UpstreamRequestID)
	}
}

// TestOpenAICompatTruncatedStreamIsNotSuccess pins the terminal-evidence
// rule. A runner or proxy that dies mid-generation closes the connection
// cleanly, so the scanner sees an ordinary EOF and the content received
// so far looks like a finished answer. Without a [DONE] sentinel or a
// finish_reason, it is a truncation, and reporting it as complete is the
// fabricated-success bug wearing better clothes.
func TestOpenAICompatTruncatedStreamIsNotSuccess(t *testing.T) {
	t.Parallel()

	content := `{"choices":[{"delta":{"content":"half an ans"}}]}`
	tests := []struct {
		name   string
		frames []string
		wantOK bool
	}{
		{name: "content then silent close", frames: []string{content}},
		{name: "content then [DONE]", frames: []string{content, "[DONE]"}, wantOK: true},
		{
			name:   "finish_reason without [DONE] is enough",
			frames: []string{content, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`},
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, f := range tt.frames {
					_, _ = w.Write([]byte("data: " + f + "\n\n"))
				}
			}))
			defer srv.Close()

			c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
			resp, err := c.ChatStream(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, func(llm.StreamEvent) {})
			if tt.wantOK {
				if err != nil {
					t.Fatalf("ChatStream: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("truncated stream reported success with content %q", resp.Message.Content)
			}
			if !strings.Contains(err.Error(), "truncated") {
				t.Errorf("error = %q, want it to name the truncation", err)
			}
		})
	}
}

// TestNormalizeOpenAICompatBaseURL pins that both documented spellings
// of a base URL reach the same endpoint. vLLM's own examples end in /v1
// and LM Studio's do not; this client appends /v1 itself, so accepting
// only the bare form turned a reasonable config into /v1/v1/... and a
// 404 naming nothing useful.
func TestNormalizeOpenAICompatBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"http://spark:8000", "http://spark:8000"},
		{"http://spark:8000/", "http://spark:8000"},
		{"http://spark:8000/v1", "http://spark:8000"},
		{"http://spark:8000/v1/", "http://spark:8000"},
		{"  http://spark:8000/v1  ", "http://spark:8000"},
	}
	for _, tt := range tests {
		if got := normalizeOpenAICompatBaseURL(tt.in); got != tt.want {
			t.Errorf("normalizeOpenAICompatBaseURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestOpenAICompatCapabilities pins that the provider advertises what
// the shared client actually implements. Declared-but-unwired was the
// shape of the earlier gap: the provider existed in the construction
// switch while the capability table still returned the zero set, so
// every deployment inherited SupportsStreaming false.
func TestOpenAICompatCapabilities(t *testing.T) {
	t.Parallel()

	caps := CapabilitiesForProvider("openai_compat")
	if !caps.SupportsChat || !caps.SupportsStreaming || !caps.SupportsTools || !caps.SupportsInventory {
		t.Fatalf("capabilities = %#v, want chat/streaming/tools/inventory", caps)
	}
	// Image support is a transport capability; the per-model gate still
	// decides whether a given model is treated as vision-capable.
	if !SupportsImagesForModel("openai_compat", "qwen3-vl:4b", "qwen3vl", nil, caps) {
		t.Error("a vision model on an image-capable transport should be vision-capable")
	}
	if SupportsImagesForModel("openai_compat", "gpt-oss:120b", "gptoss", nil, caps) {
		t.Error("a text model should not be treated as vision-capable")
	}
}

// TestOpenAICompatSendsRemainingBudget pins that a caller's remaining
// output budget reaches the wire. Before this, MaxOutputTokens was only
// checked after a response had fully arrived, so it bounded accounting
// rather than generation: one runaway completion was produced and paid
// for in full before anything noticed — minutes of wall clock on a
// runner generating single-digit tokens per second.
func TestOpenAICompatSendsRemainingBudget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		budget  int
		wantSet bool
		want    float64
	}{
		{name: "budget reaches the wire", budget: 700, wantSet: true, want: 700},
		{name: "no budget leaves the field off", budget: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer srv.Close()

			ctx := llm.WithMaxOutputTokens(context.Background(), tt.budget)
			c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
			if _, err := c.Chat(ctx, "m", []llm.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
				t.Fatalf("Chat: %v", err)
			}

			got, present := body["max_tokens"]
			if !tt.wantSet {
				if present {
					t.Errorf("max_tokens sent without a budget: %#v", got)
				}
				return
			}
			if !present || got != tt.want {
				t.Errorf("max_tokens = %#v, want %v", got, tt.want)
			}
		})
	}
}

// TestOpenAICompatStreamIdleTimeout pins the bound on silence. A server
// that accepts a request, returns headers, and then stops speaking used
// to hang the read forever: ResponseHeaderTimeout no longer applies once
// headers arrive, and these clients carry no total timeout because a
// long generation is not a hung request. The iteration never completed
// and the loop waiting on it never woke again.
func TestOpenAICompatStreamIdleTimeout(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// One frame, then silence — the shape of a runner that dies
		// mid-generation without closing the connection.
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}))
	defer srv.Close()
	defer close(release)

	c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
	c.SetStreamIdleTimeout(150 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, err := c.ChatStream(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, func(llm.StreamEvent) {})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a stalled stream returned success")
		}
		if !errors.Is(err, ErrStreamIdleTimeout) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("stalled stream error = %v, want typed deadline-exceeded idle timeout", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stalled stream was never abandoned — the idle guard did not fire")
	}
}

// TestOpenAICompatStreamIdleAllowsSlowGeneration pins the other half:
// the guard measures silence, not duration. A server that keeps sending,
// however slowly, must never be cut off — that is the behavior the
// absent total timeout exists to protect.
func TestOpenAICompatStreamIdleAllowsSlowGeneration(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		// Six frames at 60ms apart: 360ms total, comfortably past the
		// 150ms idle window, with no single gap reaching it.
		for i := 0; i < 6; i++ {
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"tok\"}}]}\n\n"))
			if f != nil {
				f.Flush()
			}
			time.Sleep(60 * time.Millisecond)
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
	c.SetStreamIdleTimeout(150 * time.Millisecond)

	resp, err := c.ChatStream(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, func(llm.StreamEvent) {})
	if err != nil {
		t.Fatalf("slow but live stream was abandoned: %v", err)
	}
	if resp.Message.Content != "toktoktoktoktoktok" {
		t.Errorf("content = %q, want all six tokens", resp.Message.Content)
	}
}

// TestOpenAICompatRequestTraceability pins what a provider call now says
// about itself. The measurements that drove the spark analysis — how
// long a call took, and how much of that was spent before the first
// token — had to be reconstructed from event JSONL because none of it
// was logged at the provider. The identifiers matter for the same
// reason: a failure at the network layer has no response body to carry
// a server-side id, so the only handle that exists on both sides is the
// one the client claimed on the way out.
func TestOpenAICompatRequestTraceability(t *testing.T) {
	t.Parallel()

	var gotClientID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClientID = r.Header.Get("X-Client-Request-Id")
		w.Header().Set("x-request-id", "srv-req-42")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-body-id","model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	ctx := logging.WithRequestID(context.Background(), "r_abc123")
	c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
	resp, err := c.Chat(ctx, "m", []llm.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if gotClientID != "r_abc123" {
		t.Errorf("X-Client-Request-Id = %q, want the caller's request id", gotClientID)
	}
	// The header wins over the completion body's id: it is present on
	// error responses too, where there is no completion object at all.
	if resp.UpstreamRequestID != "srv-req-42" {
		t.Errorf("UpstreamRequestID = %q, want the x-request-id header", resp.UpstreamRequestID)
	}
}

// TestOpenAICompatOmitsClientRequestIDWhenUnset pins that an absent id
// stays absent. A fabricated identifier matching nothing on either side
// is worse than no header — it invites correlation that cannot succeed.
func TestOpenAICompatOmitsClientRequestIDWhenUnset(t *testing.T) {
	t.Parallel()

	var present bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["X-Client-Request-Id"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
	if _, err := c.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if present {
		t.Error("X-Client-Request-Id sent without a request id in context")
	}
}

// TestOpenAICompatStreamLatencyMetrics pins the measurement this change
// exists for. Total duration cannot separate a runner that is slow from
// one that is busy — they look identical — so the completion line
// reports first-token separately from generation. It also pins the
// absence: a tool-call-only stream never produced a first token, and
// emitting a zero there would read as an instant one.
func TestOpenAICompatStreamLatencyMetrics(t *testing.T) {
	t.Parallel()

	contentFrames := []string{
		`{"choices":[{"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"delta":{"content":"hello"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		"[DONE]",
	}
	toolFrames := []string{
		`{"choices":[{"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"ha_get_state","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"[DONE]",
	}

	tests := []struct {
		name          string
		frames        []string
		wantFirstToke bool
	}{
		{name: "content stream reports first token", frames: contentFrames, wantFirstToke: true},
		{name: "tool-only stream has no first token", frames: toolFrames},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				f, _ := w.(http.Flusher)
				for _, fr := range tt.frames {
					_, _ = w.Write([]byte("data: " + fr + "\n\n"))
					if f != nil {
						f.Flush()
					}
				}
			}))
			defer srv.Close()

			buf := &bytes.Buffer{}
			logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			ctx := logging.WithLogger(context.Background(), logger)

			c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
			if _, err := c.ChatStream(ctx, "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, func(llm.StreamEvent) {}); err != nil {
				t.Fatalf("ChatStream: %v", err)
			}

			out := buf.String()
			if !strings.Contains(out, `"msg":"stream complete"`) {
				t.Fatalf("no completion line logged\n%s", out)
			}
			// The request-scoped logger must reach provider lines, or
			// they cannot be grepped alongside the turn that caused them.
			for _, want := range []string{`"total_ms"`, `"provider":"test"`, `"resource":"res"`, `"model":"m"`} {
				if !strings.Contains(out, want) {
					t.Errorf("completion line missing %s\n%s", want, out)
				}
			}
			hasFirst := strings.Contains(out, `"first_token_ms"`)
			hasGen := strings.Contains(out, `"generation_ms"`)
			if hasFirst != tt.wantFirstToke || hasGen != tt.wantFirstToke {
				t.Errorf("first_token_ms=%t generation_ms=%t, want both %t\n%s", hasFirst, hasGen, tt.wantFirstToke, out)
			}
		})
	}
}

// TestOpenAICompatReasoningOnlyStreamIsDiagnosed pins the difference
// between a model that said nothing and a model that thought hard and
// never answered. Production streams of 814 reasoning frames were
// reported as "empty" because the thinking channel wasn't decoded —
// the refusal is unchanged (there is no answer to return), but the
// diagnosis must name what actually happened.
func TestOpenAICompatReasoningOnlyStreamIsDiagnosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		frames    []string
		wantIn    string
		wantClass error // nil: the refusal must match neither completion class
		wantOK    bool
		wantOut   string
	}{
		{
			name: "ollama reasoning key with budget death",
			frames: []string{
				`{"choices":[{"delta":{"role":"assistant"}}]}`,
				`{"choices":[{"delta":{"reasoning":"first I should"}}]}`,
				`{"choices":[{"delta":{"reasoning":" consider"}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"length"}]}`,
				"[DONE]",
			},
			wantIn:    "produced only reasoning",
			wantClass: llm.ErrReasoningOnly,
		},
		{
			name: "deepseek reasoning_content key",
			frames: []string{
				`{"choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				"[DONE]",
			},
			wantIn:    "produced only reasoning",
			wantClass: llm.ErrReasoningOnly,
		},
		{
			name:      "scaffolding-only keeps the empty-stream diagnosis",
			frames:    []string{`{"choices":[{"delta":{"role":"assistant"}}]}`, "[DONE]"},
			wantIn:    "empty stream",
			wantClass: llm.ErrEmptyCompletion,
		},
		{
			name:      "finish reason with nothing said is empty",
			frames:    []string{`{"choices":[{"delta":{},"finish_reason":"stop"}]}`},
			wantIn:    "empty stream",
			wantClass: llm.ErrEmptyCompletion,
		},
		{
			// A reasoning stream that closed with no [DONE] and no
			// finish_reason is the runner/proxy dying, not a budget or
			// template problem — the reasoning-only diagnosis requires
			// terminal evidence.
			name: "reasoning then silent close is truncation, not budget death",
			frames: []string{
				`{"choices":[{"delta":{"reasoning":"deep in thought"}}]}`,
			},
			wantIn: "truncated response",
		},
		{
			// A runner or proxy that dies after the role-only opener
			// and closes cleanly said nothing and never said it was
			// done: a transport fault, not the model falling silent.
			name:   "role-only opener then silent close is not an empty completion",
			frames: []string{`{"choices":[{"delta":{"role":"assistant"}}]}`},
			wantIn: "empty stream",
		},
		{
			name:   "zero frames then silent close is not an empty completion",
			frames: nil,
			wantIn: "empty stream",
		},
		{
			name: "reasoning then content is a real completion",
			frames: []string{
				`{"choices":[{"delta":{"reasoning":"think think"}}]}`,
				`{"choices":[{"delta":{"content":"the answer"}}]}`,
				"[DONE]",
			},
			wantOK:  true,
			wantOut: "the answer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, f := range tt.frames {
					_, _ = w.Write([]byte("data: " + f + "\n\n"))
				}
			}))
			defer srv.Close()

			var streamed strings.Builder
			c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
			resp, err := c.ChatStream(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, func(ev llm.StreamEvent) {
				if ev.Kind == llm.KindToken {
					streamed.WriteString(ev.Token)
				}
			})
			if tt.wantOK {
				if err != nil {
					t.Fatalf("ChatStream: %v", err)
				}
				if resp.Message.Content != tt.wantOut {
					t.Errorf("content = %q, want %q", resp.Message.Content, tt.wantOut)
				}
				// Reasoning must not leak into the token stream.
				if streamed.String() != tt.wantOut {
					t.Errorf("streamed tokens = %q, want %q", streamed.String(), tt.wantOut)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error, got success: %#v", resp)
			}
			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Errorf("error %q missing %q", err.Error(), tt.wantIn)
			}
			assertCompletionClass(t, err, tt.wantClass)
		})
	}
}

// TestOpenAICompatReasoningOnlyCompletionIsDiagnosed covers the
// non-streaming sibling of the same seam.
func TestOpenAICompatReasoningOnlyCompletionIsDiagnosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		wantIn    []string
		wantClass error // nil: the refusal must match neither completion class
	}{
		{
			name:      "reasoning without content",
			body:      `{"model":"m","choices":[{"message":{"role":"assistant","reasoning":"deep thoughts"},"finish_reason":"length"}]}`,
			wantIn:    []string{"produced only reasoning", `finish_reason="length"`},
			wantClass: llm.ErrReasoningOnly,
		},
		{
			name:      "reasoning_content without content",
			body:      `{"model":"m","choices":[{"message":{"role":"assistant","reasoning_content":"hmm"},"finish_reason":"stop"}]}`,
			wantIn:    []string{"produced only reasoning", `finish_reason="stop"`},
			wantClass: llm.ErrReasoningOnly,
		},
		{
			name:      "nothing said at all",
			body:      `{"model":"m","choices":[{"message":{"role":"assistant","content":"  "},"finish_reason":"stop"}]}`,
			wantIn:    []string{`test returned an empty assistant completion for model "m"`},
			wantClass: llm.ErrEmptyCompletion,
		},
		{
			// A response with no choices is malformed, not an empty
			// completion: no message was offered to judge.
			name:   "no choices is not a completion class",
			body:   `{"model":"m","choices":[]}`,
			wantIn: []string{"response contained no choices"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
			_, err := c.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil)
			if err == nil {
				t.Fatal("want error for a completion with no answer")
			}
			for _, want := range tt.wantIn {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err.Error(), want)
				}
			}
			assertCompletionClass(t, err, tt.wantClass)
		})
	}
}

// assertCompletionClass checks that err matches exactly the wanted
// completion class under errors.Is, or neither class when want is nil.
// The classes are disjoint: a reasoning-only refusal is not an empty
// one, and a truncation or malformed response is neither.
func assertCompletionClass(t *testing.T, err, want error) {
	t.Helper()
	for _, class := range []error{llm.ErrEmptyCompletion, llm.ErrReasoningOnly} {
		if got := errors.Is(err, class); got != (class == want) {
			t.Errorf("errors.Is(%q, %v) = %t, want %t", err, class, got, class == want)
		}
	}
}

// TestOpenAICompatSendsChatTemplateKwargs pins the reasoning toggle onto
// the wire. A model whose chat template enables thinking by default
// spends the output budget on a think block first, so a turn with a
// modest ceiling returns finish_reason "length" with empty content and
// no answer at all — the guard converts that to an error, which is
// correct and still a lost turn. chat_template_kwargs is the only lever
// that turns it off, and before this the field could not be sent.
func TestOpenAICompatSendsChatTemplateKwargs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		kwargs map[string]any
		stream bool
		want   map[string]any
	}{
		{
			name:   "configured kwargs reach the wire",
			kwargs: map[string]any{"enable_thinking": false},
			want:   map[string]any{"enable_thinking": false},
		},
		{
			name:   "the streaming path sends them too",
			kwargs: map[string]any{"enable_thinking": false},
			stream: true,
			want:   map[string]any{"enable_thinking": false},
		},
		{
			// Families disagree on the key, so a resource is allowed to
			// send several and let the template read the one it knows.
			name:   "several keys pass through untouched",
			kwargs: map[string]any{"enable_thinking": false, "thinking": false, "reasoning_effort": "low"},
			want:   map[string]any{"enable_thinking": false, "thinking": false, "reasoning_effort": "low"},
		},
		{
			name: "unconfigured leaves the field off",
		},
		{
			// Not the same as sending {}: a server entitled to validate
			// the field should never see an empty object from us.
			name:   "an empty map leaves the field off",
			kwargs: map[string]any{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&body)
				if tt.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer srv.Close()

			c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
			c.SetChatTemplateKwargs(tt.kwargs)

			var cb llm.StreamCallback
			if tt.stream {
				cb = func(llm.StreamEvent) {}
			}
			if _, err := c.ChatStream(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, cb); err != nil {
				t.Fatalf("ChatStream: %v", err)
			}

			got, present := body["chat_template_kwargs"]
			if tt.want == nil {
				if present {
					t.Errorf("chat_template_kwargs sent unconfigured: %#v", got)
				}
				return
			}
			gotMap, ok := got.(map[string]any)
			if !ok {
				t.Fatalf("chat_template_kwargs = %#v, want an object", got)
			}
			if !reflect.DeepEqual(gotMap, tt.want) {
				t.Errorf("chat_template_kwargs = %#v, want %#v", gotMap, tt.want)
			}
		})
	}
}

// TestOpenAICompatChatTemplateKwargsCopiesCaller pins that the client
// holds its own copy. The map reaches it from config, which other
// readers share; a client that aliased it would let an unrelated write
// change what every later turn asks the model to do.
func TestOpenAICompatChatTemplateKwargsCopiesCaller(t *testing.T) {
	t.Parallel()

	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	// Nested values as well as top-level ones: a kwarg may itself be an
	// object or a list, and a shallow copy would leave those aliased to
	// config however carefully the outer map was copied.
	nested := map[string]any{"depth": "original"}
	list := []any{"first"}
	typed := []string{"typed"}
	box := &privateBox{m: map[string]string{"k": "original"}}
	caller := map[string]any{
		"enable_thinking": false,
		"options":         nested,
		"stops":           list,
		"typed":           typed,
		"box":             box,
	}
	c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
	c.SetChatTemplateKwargs(caller)

	caller["enable_thinking"] = true
	nested["depth"] = "mutated"
	list[0] = "mutated"
	typed[0] = "mutated"
	box.Set("k", "mutated")

	if _, err := c.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	got, _ := body["chat_template_kwargs"].(map[string]any)
	want := map[string]any{
		"enable_thinking": false,
		"options":         map[string]any{"depth": "original"},
		"stops":           []any{"first"},
		"typed":           []any{"typed"},
		"box":             map[string]any{"k": "original"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("chat_template_kwargs = %#v, want %#v — a caller mutation leaked in", got, want)
	}
}

// wireFixtureMessages is a history that exercises every map-valued
// field the encoder writes: tool call arguments nested several levels
// deep, beside plain text and a tool result.
func wireFixtureMessages() []llm.Message {
	call := llm.ToolCall{ID: "call_1"}
	call.Function.Name = "lookup_place"
	call.Function.Arguments = map[string]any{
		"zeta":   "last",
		"alpha":  1,
		"middle": map[string]any{"y": []any{"b", "a"}, "x": true},
		"beta":   nil,
	}
	return []llm.Message{
		{Role: "system", Content: "You are a test fixture."},
		{Role: "user", Content: "Where is Alice?"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{call}},
		{Role: "tool", ToolCallID: "call_1", Content: `{"place":"garden"}`},
	}
}

// wireFixtureTools returns a tool surface whose description can be
// varied, so a test can show that one changed description changes the
// digest.
func wireFixtureTools(description string) []map[string]any {
	return []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name":        "lookup_place",
			"description": description,
			"parameters": map[string]any{
				"type":     "object",
				"required": []any{"name"},
				"properties": map[string]any{
					"name":  map[string]any{"type": "string"},
					"limit": map[string]any{"type": "integer"},
				},
			},
		},
	}}
}

// wireRecorder is an OpenAI-compatible server that keeps the exact
// bytes of every request body it receives and answers in whichever
// shape the request asked for.
type wireRecorder struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (r *wireRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.bodies = append(r.bodies, body)
	r.mu.Unlock()

	var shape struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &shape)
	if shape.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
}

func (r *wireRecorder) received() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.bodies...)
}

// TestEncodeChatRequestIsWhatChatStreamSends pins the property replay
// verification rests on: the bytes a server receives are exactly what
// EncodeChatRequest returns for the same arguments, and the observer's
// digest is the digest of those bytes. If ChatStream ever re-encoded,
// added a field after encoding, or reported a digest of something other
// than the body, a replay could no longer prove what production sent.
// LM Studio is covered separately because it reaches ChatStream through
// embedding, and an override there would silently lose the observer.
func TestEncodeChatRequestIsWhatChatStreamSends(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		lmstudio bool
		stream   bool
		ttl      int
		model    string
		provider string
	}{
		{name: "openai_compat non-streaming", model: "Example/Model-1", provider: "vllm"},
		{name: "openai_compat streaming", stream: true, model: "Example/Model-1", provider: "vllm"},
		{name: "lmstudio non-streaming", lmstudio: true, ttl: 900, model: "example-model:2", provider: "lmstudio"},
		{name: "lmstudio streaming", lmstudio: true, stream: true, ttl: 900, model: "example-model:2", provider: "lmstudio"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &wireRecorder{}
			srv := httptest.NewServer(rec)
			defer srv.Close()

			kwargs := map[string]any{"enable_thinking": false, "options": map[string]any{"depth": 2}}
			var (
				chat   func(context.Context, string, []llm.Message, []map[string]any, llm.StreamCallback) (*llm.ChatResponse, error)
				encode func(context.Context, string, []llm.Message, []map[string]any, bool) ([]byte, llm.WireFacts, error)
			)
			if tt.lmstudio {
				lm := NewLMStudioClientWithTTL(srv.URL, "", "res-lm", nil, tt.ttl)
				lm.SetChatTemplateKwargs(kwargs)
				chat, encode = lm.ChatStream, lm.EncodeChatRequest
			} else {
				oc := NewOpenAICompatClient(srv.URL, "", tt.provider, "res-oc", nil, tt.ttl)
				oc.SetChatTemplateKwargs(kwargs)
				chat, encode = oc.ChatStream, oc.EncodeChatRequest
			}

			var observed []llm.WireFacts
			ctx := llm.WithMaxOutputTokens(context.Background(), 2048)
			ctx = llm.WithWireObserver(ctx, func(f llm.WireFacts) { observed = append(observed, f) })

			var callback llm.StreamCallback
			if tt.stream {
				callback = func(llm.StreamEvent) {}
			}
			messages, tools := wireFixtureMessages(), wireFixtureTools("Find where a person is.")
			if _, err := chat(ctx, tt.model, messages, tools, callback); err != nil {
				t.Fatalf("ChatStream: %v", err)
			}

			want, wantFacts, err := encode(ctx, tt.model, wireFixtureMessages(), wireFixtureTools("Find where a person is."), tt.stream)
			if err != nil {
				t.Fatalf("EncodeChatRequest: %v", err)
			}
			bodies := rec.received()
			if len(bodies) != 1 {
				t.Fatalf("server received %d requests, want 1", len(bodies))
			}
			if !bytes.Equal(bodies[0], want) {
				t.Fatalf("received body differs from EncodeChatRequest output\nreceived: %s\nencoded:  %s", bodies[0], want)
			}

			if len(observed) != 1 {
				t.Fatalf("observer called %d times, want 1", len(observed))
			}
			got := observed[0]
			sum := sha256.Sum256(bodies[0])
			if got.BodySHA256 != hex.EncodeToString(sum[:]) {
				t.Errorf("observer digest = %s, want sha256 of the received body %x", got.BodySHA256, sum)
			}
			if got.BodyBytes != len(bodies[0]) {
				t.Errorf("observer length = %d, want %d", got.BodyBytes, len(bodies[0]))
			}
			if !reflect.DeepEqual(got, wantFacts) {
				t.Errorf("observed facts = %+v, want the encoder's %+v", got, wantFacts)
			}

			wantStatic := llm.WireFacts{
				Provider:   tt.provider,
				Resource:   map[bool]string{true: "res-lm", false: "res-oc"}[tt.lmstudio],
				Model:      tt.model,
				TTLSeconds: tt.ttl,
				MaxTokens:  2048,
				Stream:     tt.stream,
				// Facts carry the kwargs as plain JSON data: the number
				// keeps its literal text as a json.Number.
				ChatTemplateKwargs: map[string]any{"enable_thinking": false, "options": map[string]any{"depth": json.Number("2")}},
				BodySHA256:         got.BodySHA256,
				BodyBytes:          got.BodyBytes,
			}
			if !reflect.DeepEqual(got, wantStatic) {
				t.Errorf("observed facts = %+v, want %+v", got, wantStatic)
			}

			// The facts must agree with the body they describe, not
			// only with the client's configuration.
			var sent map[string]any
			if err := json.Unmarshal(bodies[0], &sent); err != nil {
				t.Fatalf("decode received body: %v", err)
			}
			if sent["model"] != tt.model {
				t.Errorf("body model = %v, want %q", sent["model"], tt.model)
			}
			if !reflect.DeepEqual(sent["chat_template_kwargs"], map[string]any{"enable_thinking": false, "options": map[string]any{"depth": float64(2)}}) {
				t.Errorf("body chat_template_kwargs = %#v", sent["chat_template_kwargs"])
			}
			// The encoder omits zero values, so an absent key reads as the
			// zero fact. Numbers decode as float64.
			bodyInt := func(key string) int {
				v, _ := sent[key].(float64)
				return int(v)
			}
			bodyStream, _ := sent["stream"].(bool)
			if bodyInt("ttl") != got.TTLSeconds {
				t.Errorf("body ttl = %d, facts say %d", bodyInt("ttl"), got.TTLSeconds)
			}
			if bodyInt("max_tokens") != got.MaxTokens {
				t.Errorf("body max_tokens = %d, facts say %d", bodyInt("max_tokens"), got.MaxTokens)
			}
			if bodyStream != got.Stream {
				t.Errorf("body stream = %v, facts say %v", bodyStream, got.Stream)
			}
		})
	}
}

// TestEncodeChatRequestDeterministic pins that equal arguments encode
// to equal bytes even though Go randomizes map iteration: tool call
// arguments, tool schemas, and template kwargs are all maps, and a
// digest that varied run to run would make every replay a mismatch.
// The same table also serves as the negative control: changing any one
// input a replay must reproduce changes the digest.
func TestEncodeChatRequestDeterministic(t *testing.T) {
	t.Parallel()

	newClient := func(ttl int, kwargs map[string]any) *OpenAICompatClient {
		c := NewOpenAICompatClient("http://example.invalid", "", "vllm", "res", nil, ttl)
		c.SetChatTemplateKwargs(kwargs)
		return c
	}
	baseKwargs := func() map[string]any {
		return map[string]any{"enable_thinking": false, "b": map[string]any{"z": 1, "a": 2}}
	}
	encode := func(t *testing.T, c *OpenAICompatClient, maxTokens int, description string, stream bool) llm.WireFacts {
		t.Helper()
		ctx := llm.WithMaxOutputTokens(context.Background(), maxTokens)
		_, facts, err := c.EncodeChatRequest(ctx, "Example/Model-1", wireFixtureMessages(), wireFixtureTools(description), stream)
		if err != nil {
			t.Fatalf("EncodeChatRequest: %v", err)
		}
		return facts
	}

	base := newClient(0, baseKwargs())
	want := encode(t, base, 1024, "Find where a person is.", false)
	for i := range 50 {
		// A fresh client and freshly built maps each time, so nothing
		// is shared between encodings but the values themselves.
		if got := encode(t, newClient(0, baseKwargs()), 1024, "Find where a person is.", false); got.BodySHA256 != want.BodySHA256 {
			t.Fatalf("encoding %d digest = %s, want %s: equal arguments encoded differently", i, got.BodySHA256, want.BodySHA256)
		}
	}

	tests := []struct {
		name  string
		facts llm.WireFacts
	}{
		{name: "one tool description", facts: encode(t, base, 1024, "Find where someone is.", false)},
		{name: "max_tokens", facts: encode(t, base, 1023, "Find where a person is.", false)},
		{name: "no max_tokens", facts: encode(t, base, 0, "Find where a person is.", false)},
		{name: "ttl", facts: encode(t, newClient(60, baseKwargs()), 1024, "Find where a person is.", false)},
		{name: "kwargs value", facts: encode(t, newClient(0, map[string]any{"enable_thinking": true, "b": map[string]any{"z": 1, "a": 2}}), 1024, "Find where a person is.", false)},
		{name: "no kwargs", facts: encode(t, newClient(0, nil), 1024, "Find where a person is.", false)},
		{name: "stream", facts: encode(t, base, 1024, "Find where a person is.", true)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.facts.BodySHA256 == want.BodySHA256 {
				t.Errorf("changing %s left the digest at %s; a replay could not detect it", tt.name, want.BodySHA256)
			}
		})
	}
}

// TestWireObserverNegativeCases pins when the observer must stay
// silent and what it must not be able to reach: a request that fails to
// encode was never sent and reports nothing, a call without an observer
// still works, and facts handed to an observer are its own copy. It also
// pins the one case where an observation outlives the post: a transport
// failure after the observer fired leaves the facts recorded, which is
// why they must be read with the call's error.
func TestWireObserverNegativeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		tools        []map[string]any
		observe      bool
		wantErr      string
		transportErr bool
		wantRequests int
		wantObserved int
	}{
		{name: "unencodable tools", tools: []map[string]any{{"bad": make(chan int)}}, observe: true, wantErr: "marshal request", wantRequests: 0, wantObserved: 0},
		{name: "no observer attached", tools: wireFixtureTools("Find where a person is."), observe: false, wantRequests: 1, wantObserved: 0},
		{name: "observer attached", tools: wireFixtureTools("Find where a person is."), observe: true, wantRequests: 1, wantObserved: 1},
		{name: "transport failure", tools: wireFixtureTools("Find where a person is."), observe: true, transportErr: true, wantErr: "request failed", wantRequests: 0, wantObserved: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &wireRecorder{}
			srv := httptest.NewServer(rec)
			defer srv.Close()

			c := NewOpenAICompatClient(srv.URL, "", "vllm", "res", nil, 0)
			// Typed values as well as untyped ones: a programmatic caller
			// can hand over a []string or map[string]string, and those
			// must be detached as thoroughly as decoded config.
			c.SetChatTemplateKwargs(map[string]any{
				"options": map[string]any{"depth": "original"},
				"stops":   []string{"original"},
				"labels":  map[string]string{"k": "original"},
			})
			if tt.transportErr {
				// A transport that never reaches the server, without the
				// production retry backoff in the way.
				c.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("connection refused")
				})}
			}

			var observed []llm.WireFacts
			ctx := context.Background()
			if tt.observe {
				ctx = llm.WithWireObserver(ctx, func(f llm.WireFacts) {
					observed = append(observed, f)
					// An observer scribbling on what it was handed
					// must not change what later requests send.
					if opts, ok := f.ChatTemplateKwargs["options"].(map[string]any); ok {
						opts["depth"] = "mutated"
					}
					// Facts carry plain JSON data, whatever types the
					// configuration was built from.
					if stops, ok := f.ChatTemplateKwargs["stops"].([]any); ok {
						stops[0] = "mutated"
					}
					if labels, ok := f.ChatTemplateKwargs["labels"].(map[string]any); ok {
						labels["k"] = "mutated"
					}
				})
			}

			_, err := c.Chat(ctx, "m", wireFixtureMessages(), tt.tools)
			switch {
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("Chat error = %v, want one naming %q", err, tt.wantErr)
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Chat: %v", err)
			}
			if got := len(rec.received()); got != tt.wantRequests {
				t.Errorf("server received %d requests, want %d", got, tt.wantRequests)
			}
			if len(observed) != tt.wantObserved {
				t.Errorf("observer called %d times, want %d", len(observed), tt.wantObserved)
			}

			_, facts, err := c.EncodeChatRequest(context.Background(), "m", wireFixtureMessages(), wireFixtureTools("x"), false)
			if err != nil {
				t.Fatalf("EncodeChatRequest: %v", err)
			}
			wantKwargs := map[string]any{
				"options": map[string]any{"depth": "original"},
				"stops":   []any{"original"},
				"labels":  map[string]any{"k": "original"},
			}
			if !reflect.DeepEqual(facts.ChatTemplateKwargs, wantKwargs) {
				t.Errorf("kwargs after observation = %#v: an observer reached the client's configuration", facts.ChatTemplateKwargs)
			}
		})
	}
}
