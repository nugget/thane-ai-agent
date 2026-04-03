package llm

import (
	"context"
	"strings"
	"testing"
)

type recordingClient struct {
	lastModel string
}

func (c *recordingClient) Chat(_ context.Context, model string, _ []Message, _ []map[string]any) (*ChatResponse, error) {
	c.lastModel = model
	return &ChatResponse{
		Model:   model,
		Message: Message{Role: "assistant", Content: "ok"},
		Done:    true,
	}, nil
}

func (c *recordingClient) ChatStream(ctx context.Context, model string, messages []Message, tools []map[string]any, callback StreamCallback) (*ChatResponse, error) {
	return c.Chat(ctx, model, messages, tools)
}

func (c *recordingClient) Ping(context.Context) error {
	return nil
}

func TestMultiClientChat_RoutesAliasToUpstreamModel(t *testing.T) {
	client := &recordingClient{}
	multi := NewMultiClient(nil)
	multi.AddProvider("edge", client)
	multi.AddRoute("edge/qwen3:4b", "edge", "qwen3:4b")
	multi.AddAlias("fast_local", "edge/qwen3:4b")

	resp, err := multi.Chat(context.Background(), "fast_local", nil, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if client.lastModel != "qwen3:4b" {
		t.Fatalf("provider received model %q, want %q", client.lastModel, "qwen3:4b")
	}
	if resp.Model != "qwen3:4b" {
		t.Fatalf("response model = %q, want %q", resp.Model, "qwen3:4b")
	}
}

func TestMultiClientChat_RejectsAmbiguousAlias(t *testing.T) {
	multi := NewMultiClient(nil)
	multi.MarkAmbiguous("qwen3:4b", []string{"default/qwen3:4b", "edge/qwen3:4b"})

	_, err := multi.Chat(context.Background(), "qwen3:4b", nil, nil)
	if err == nil {
		t.Fatal("Chat() should fail for ambiguous alias")
	}
	if msg := err.Error(); !strings.Contains(msg, "default/qwen3:4b") || !strings.Contains(msg, "edge/qwen3:4b") {
		t.Fatalf("Chat() error = %q, want both qualified ids", msg)
	}
}

func TestMultiClientChat_UsesFallbackForUnknownModel(t *testing.T) {
	client := &recordingClient{}
	multi := NewMultiClient(client)

	resp, err := multi.Chat(context.Background(), "unknown-model", nil, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if client.lastModel != "unknown-model" {
		t.Fatalf("fallback received model %q, want %q", client.lastModel, "unknown-model")
	}
	if resp.Model != "unknown-model" {
		t.Fatalf("response model = %q, want %q", resp.Model, "unknown-model")
	}
}
