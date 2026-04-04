package llm

import (
	"context"
	"testing"
)

func TestDynamicClientSwap(t *testing.T) {
	t.Parallel()

	first := &recordingClient{}
	second := &recordingClient{}

	client := NewDynamicClient(first)

	resp, err := client.Chat(context.Background(), "model-a", nil, nil)
	if err != nil {
		t.Fatalf("first Chat() error = %v", err)
	}
	if resp.Model != "model-a" || first.lastModel != "model-a" {
		t.Fatalf("first client did not receive original request: resp=%q last=%q", resp.Model, first.lastModel)
	}

	if err := client.Swap(second); err != nil {
		t.Fatalf("Swap() error = %v", err)
	}

	resp, err = client.Chat(context.Background(), "model-b", nil, nil)
	if err != nil {
		t.Fatalf("second Chat() error = %v", err)
	}
	if resp.Model != "model-b" || second.lastModel != "model-b" {
		t.Fatalf("second client did not receive swapped request: resp=%q last=%q", resp.Model, second.lastModel)
	}
	if first.lastModel != "model-a" {
		t.Fatalf("first client should not receive post-swap request, last=%q", first.lastModel)
	}
}
