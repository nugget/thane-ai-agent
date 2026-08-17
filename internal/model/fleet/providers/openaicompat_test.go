package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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
