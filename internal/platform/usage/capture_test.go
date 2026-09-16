package usage_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
)

func TestCallRecord(t *testing.T) {
	tests := []struct {
		name     string
		response *llm.ChatResponse
		err      error
		outcome  string
	}{
		{name: "no response"},
		{name: "missing usage", response: &llm.ChatResponse{Model: "synthetic"}},
		{name: "success", response: &llm.ChatResponse{InputTokens: 3}, outcome: "success"},
		{name: "partial failure", response: &llm.ChatResponse{OutputTokens: 4}, err: errors.New("stream interrupted"), outcome: "error"},
		{name: "canceled", response: &llm.ChatResponse{CacheReadInputTokens: 5}, err: fmt.Errorf("read: %w", context.Canceled), outcome: "canceled"},
		{name: "deadline", response: &llm.ChatResponse{CacheCreationInputTokens: 6}, err: context.DeadlineExceeded, outcome: "canceled"},
		{name: "5m only", response: &llm.ChatResponse{CacheCreation5mInputTokens: 7}, outcome: "success"},
		{name: "1h only", response: &llm.ChatResponse{CacheCreation1hInputTokens: 8}, outcome: "success"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := time.Now().Add(-time.Second)
			record, ok := usage.CallRecord("selected", tt.response, started, tt.err)
			if ok != (tt.outcome != "") {
				t.Fatalf("recorded = %v, want %v", ok, tt.outcome != "")
			}
			if !ok {
				return
			}
			if record.Outcome != tt.outcome || record.Model != "selected" || record.DurationMS < 1000 || record.Timestamp != started {
				t.Fatalf("unexpected call metadata: %+v", record)
			}
			r := tt.response
			if record.InputTokens != r.InputTokens || record.OutputTokens != r.OutputTokens ||
				record.CacheReadInputTokens != r.CacheReadInputTokens || record.CacheCreationInputTokens != r.CacheCreationInputTokens ||
				record.CacheCreation5mInputTokens != r.CacheCreation5mInputTokens || record.CacheCreation1hInputTokens != r.CacheCreation1hInputTokens {
				t.Fatalf("usage counters changed: %+v", record)
			}
		})
	}
}

type captureClient struct {
	response *llm.ChatResponse
	err      error
	ctx      context.Context
	model    string
	streamed bool
	pinged   bool
}

func (c *captureClient) Chat(ctx context.Context, model string, _ []llm.Message, _ []map[string]any) (*llm.ChatResponse, error) {
	c.ctx, c.model = ctx, model
	return c.response, c.err
}

func (c *captureClient) ChatStream(ctx context.Context, model string, messages []llm.Message, tools []map[string]any, callback llm.StreamCallback) (*llm.ChatResponse, error) {
	c.streamed = true
	if callback != nil {
		callback(llm.StreamEvent{Response: c.response})
	}
	return c.Chat(ctx, model, messages, tools)
}

func (c *captureClient) Ping(context.Context) error {
	c.pinged = true
	return c.err
}

func TestTrackingClientPreservesPartialUsageAndAttribution(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = llm.WithAttribution(ctx, llm.Attribution{
				RequestID: "request-full", SessionID: "session-full", ConversationID: "conversation-full",
				LoopID: "loop-full", LoopName: "reflector", ParentLoopID: "parent-full",
			})
			ctx = usage.WithObserver(ctx, func(usage.Record) { t.Fatal("auxiliary capture notified live request observer") })
			cancel()
			response := &llm.ChatResponse{Model: "resolved-deployment", InputTokens: 11, UpstreamRequestID: "provider-request"}
			underlying := &captureClient{response: response, err: context.Canceled}
			var records []usage.Record
			client := usage.TrackingClient(underlying, func(callCtx context.Context, record usage.Record) {
				if callCtx != ctx {
					t.Fatal("capture lost original context")
				}
				records = append(records, record)
			})
			var got *llm.ChatResponse
			var err error
			callbackCount := 0
			if streaming {
				got, err = client.ChatStream(ctx, "alias", nil, nil, func(event llm.StreamEvent) {
					callbackCount++
					if event.Response != response {
						t.Fatal("stream response changed")
					}
				})
			} else {
				got, err = client.Chat(ctx, "alias", nil, nil)
			}
			if got != response || err != context.Canceled || underlying.ctx != ctx || underlying.model != "alias" {
				t.Fatal("wrapper changed call arguments or result")
			}
			if underlying.streamed != streaming || (streaming && callbackCount != 1) || len(records) != 1 {
				t.Fatalf("streamed=%v callback count=%d records=%d", underlying.streamed, callbackCount, len(records))
			}
			r := records[0]
			if r.Model != "resolved-deployment" || r.UpstreamRequestID != "provider-request" || r.Outcome != "canceled" ||
				r.RequestID != "request-full" || r.SessionID != "session-full" || r.ConversationID != "conversation-full" ||
				r.LoopID != "loop-full" || r.LoopName != "reflector" || r.ParentLoopID != "parent-full" {
				t.Fatalf("capture lost attribution: %+v", r)
			}
			if err := client.Ping(ctx); err != context.Canceled || !underlying.pinged || len(records) != 1 {
				t.Fatal("Ping should pass through without usage")
			}
		})
	}
}

func TestTrackingClientMissingUsage(t *testing.T) {
	underlying := &captureClient{response: &llm.ChatResponse{Message: llm.Message{Content: "fallback"}}}
	client := usage.TrackingClient(underlying, func(context.Context, usage.Record) { t.Fatal("invented usage record") })
	if _, err := client.Chat(context.Background(), "model", nil, nil); err != nil {
		t.Fatal(err)
	}
	if usage.TrackingClient(underlying, nil) != underlying {
		t.Fatal("nil callback should return original client")
	}
}

func TestAttributionPreservesExplicitIdentifiers(t *testing.T) {
	ctx := llm.WithAttribution(context.Background(), llm.Attribution{SessionID: "parent-session", LoopID: "loop"})
	ctx = context.WithoutCancel(ctx)
	r := usage.ApplyAttribution(usage.Record{SessionID: "child-session"}, llm.AttributionFromContext(ctx))
	if r.SessionID != "child-session" || r.LoopID != "loop" {
		t.Fatalf("unexpected attribution: %+v", r)
	}
	if a := llm.AttributionFromContext(context.Background()); a != (llm.Attribution{}) {
		t.Fatalf("missing attribution = %+v", a)
	}
}
