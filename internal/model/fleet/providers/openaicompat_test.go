package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
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

			c := NewOpenAICompatClient(srv.URL, "", "test", nil, 0)
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

	c := NewOpenAICompatClient(srv.URL, "", "test", nil, 0)
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
