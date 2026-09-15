package llm

import (
	"context"
	"errors"
	"testing"
)

type partialUsageClient struct {
	failure error
}

func (c partialUsageClient) Chat(_ context.Context, model string, _ []Message, _ []map[string]any) (*ChatResponse, error) {
	return &ChatResponse{Model: model, InputTokens: 123, OutputTokens: 45, UpstreamRequestID: "provider-request"}, c.failure
}

func (c partialUsageClient) ChatStream(ctx context.Context, model string, messages []Message, tools []map[string]any, _ StreamCallback) (*ChatResponse, error) {
	return c.Chat(ctx, model, messages, tools)
}

func (c partialUsageClient) Ping(context.Context) error { return nil }

func TestMultiClientPreservesPartialResponseIdentity(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "chat"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			failure := errors.New("provider interrupted")
			multi := NewMultiClient(nil)
			multi.AddProvider("edge", partialUsageClient{failure: failure})
			multi.AddRoute("edge/model", "edge", "upstream-model")
			multi.AddAlias("selected-model", "edge/model")
			var response *ChatResponse
			var err error
			if stream {
				response, err = multi.ChatStream(context.Background(), "selected-model", nil, nil, nil)
			} else {
				response, err = multi.Chat(context.Background(), "selected-model", nil, nil)
			}
			if !errors.Is(err, failure) || response == nil || response.Model != "edge/model" || response.InputTokens != 123 || response.OutputTokens != 45 || response.UpstreamRequestID != "provider-request" {
				t.Fatalf("partial response or route identity lost: response=%+v error=%v", response, err)
			}
		})
	}
}
