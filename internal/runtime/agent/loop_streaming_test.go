package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
)

type streamingLLM struct {
	afterFirstToken func()
}

func (m *streamingLLM) Chat(ctx context.Context, model string, msgs []llm.Message, tools []map[string]any) (*llm.ChatResponse, error) {
	return m.ChatStream(ctx, model, msgs, tools, nil)
}

func (m *streamingLLM) ChatStream(_ context.Context, model string, _ []llm.Message, _ []map[string]any, callback llm.StreamCallback) (*llm.ChatResponse, error) {
	if callback != nil {
		callback(llm.StreamEvent{Kind: llm.KindToken, Token: "hello "})
		if m.afterFirstToken != nil {
			m.afterFirstToken()
		}
		callback(llm.StreamEvent{Kind: llm.KindToken, Token: "world"})
	}
	return &llm.ChatResponse{
		Model: model,
		Message: llm.Message{
			Role:    "assistant",
			Content: "hello world",
		},
		InputTokens:  11,
		OutputTokens: 2,
	}, nil
}

func (m *streamingLLM) Ping(_ context.Context) error { return nil }

func TestLoopRun_LiveRequestRecorderTracksStreamedContent(t *testing.T) {
	t.Parallel()

	mock := &streamingLLM{}
	loop := buildTestLoopWithLLM(mock, nil)

	var (
		mu     sync.Mutex
		latest logging.RequestContent
	)
	loop.UseLiveRequestRecorder(func(_ context.Context, rc logging.RequestContent) {
		mu.Lock()
		defer mu.Unlock()
		latest = rc
	})

	mock.afterFirstToken = func() {
		mu.Lock()
		defer mu.Unlock()
		if latest.AssistantContent != "hello " {
			t.Fatalf("mid-stream assistant content = %q, want %q", latest.AssistantContent, "hello ")
		}
		if latest.Model != "test-model" {
			t.Fatalf("mid-stream model = %q, want %q", latest.Model, "test-model")
		}
	}

	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "tell me something nice"}},
	}, func(StreamEvent) {})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if resp.Content != "hello world" {
		t.Fatalf("response content = %q, want %q", resp.Content, "hello world")
	}

	mu.Lock()
	defer mu.Unlock()
	if latest.AssistantContent != "hello world" {
		t.Fatalf("final assistant content = %q, want %q", latest.AssistantContent, "hello world")
	}
	if latest.InputTokens != 11 {
		t.Fatalf("final input tokens = %d, want %d", latest.InputTokens, 11)
	}
	if latest.OutputTokens != 2 {
		t.Fatalf("final output tokens = %d, want %d", latest.OutputTokens, 2)
	}
	if latest.IterationCount != 1 {
		t.Fatalf("final iteration count = %d, want %d", latest.IterationCount, 1)
	}
}
