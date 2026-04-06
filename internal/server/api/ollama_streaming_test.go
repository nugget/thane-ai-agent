package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
)

type streamingResponseRecorder struct {
	mu      sync.Mutex
	header  http.Header
	body    bytes.Buffer
	code    int
	flushed int
}

func newStreamingResponseRecorder() *streamingResponseRecorder {
	return &streamingResponseRecorder{
		header: make(http.Header),
	}
}

func (r *streamingResponseRecorder) Header() http.Header {
	return r.header
}

func (r *streamingResponseRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(b)
}

func (r *streamingResponseRecorder) WriteHeader(statusCode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.code = statusCode
}

func (r *streamingResponseRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushed++
}

func (r *streamingResponseRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

func TestHandleOllamaStreamingChatShared_StreamsFirstTokenImmediately(t *testing.T) {
	t.Parallel()

	rec := newStreamingResponseRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
	agentReq := &agent.Request{
		Messages: []agent.Message{{Role: "user", Content: "stream something"}},
	}

	firstTokenWritten := make(chan struct{})
	unblockRun := make(chan struct{})
	done := make(chan struct{})
	firstTokenBody := make(chan string, 1)

	run := func(_ context.Context, _ *agent.Request, cb agent.StreamCallback) (*agent.Response, error) {
		initial := rec.String()
		if !strings.Contains(initial, `"content":""`) {
			t.Fatalf("body before first token = %q, want initial empty chunk", initial)
		}
		if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
			t.Fatalf("X-Accel-Buffering = %q, want %q", got, "no")
		}
		cb(agent.StreamEvent{Kind: agent.KindToken, Token: "hello"})
		firstTokenBody <- rec.String()
		close(firstTokenWritten)
		<-unblockRun
		return &agent.Response{
			Content:      "hello",
			Model:        "thane:latest",
			FinishReason: "stop",
		}, nil
	}

	go func() {
		handleOllamaStreamingChatShared(rec, req, agentReq, time.Now(), run, slog.Default())
		close(done)
	}()

	select {
	case <-firstTokenWritten:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first streamed token")
	}
	got := <-firstTokenBody
	if !strings.Contains(got, `"content":"hello"`) {
		t.Fatalf("body after first token = %q, want streamed token chunk", got)
	}

	close(unblockRun)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for streaming handler to finish")
	}
}
