package usage

import (
	"context"
	"errors"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// CallRecord extracts provider-reported usage from one completed client call,
// including a partial response returned with an error. The boolean is false
// when no nonzero token counter was reported: missing usage cannot establish
// that a call was free. Model is the response deployment when available, or
// the requested model otherwise. DurationMS measures client-call wall time;
// Outcome describes this call, not the enclosing request or loop wake.
// The returned record is neither priced, attributed, observed, nor persisted.
func CallRecord(model string, response *llm.ChatResponse, started time.Time, err error) (Record, bool) {
	if response == nil || (response.InputTokens == 0 && response.OutputTokens == 0 &&
		response.CacheCreationInputTokens == 0 && response.CacheReadInputTokens == 0 &&
		response.CacheCreation5mInputTokens == 0 && response.CacheCreation1hInputTokens == 0) {
		return Record{}, false
	}
	if response.Model != "" {
		model = response.Model
	}
	outcome := "success"
	if err != nil {
		outcome = "error"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			outcome = "canceled"
		}
	}
	return Record{
		Timestamp: started, Model: model, UpstreamRequestID: response.UpstreamRequestID,
		InputTokens: response.InputTokens, OutputTokens: response.OutputTokens,
		CacheCreationInputTokens:   response.CacheCreationInputTokens,
		CacheCreation5mInputTokens: response.CacheCreation5mInputTokens,
		CacheCreation1hInputTokens: response.CacheCreation1hInputTokens,
		CacheReadInputTokens:       response.CacheReadInputTokens,
		Outcome:                    outcome, DurationMS: max(0, time.Since(started).Milliseconds()),
	}, true
}

// TrackingClient wraps an otherwise unaccounted client and synchronously
// delivers reported usage to record after each Chat or ChatStream call. The
// callback receives [llm.Attribution] from the call context and must be safe for
// concurrent calls. It owns pricing and persistence, including any bounded
// write after cancellation. A nil callback leaves the client unchanged.
//
// This adapter does not publish to [WithObserver] request statistics. Use it
// for auxiliary work outside the agent's existing per-call accounting boundary;
// wrapping an already-accounted client would duplicate records. The underlying
// response, error, streaming callback, and Ping behavior are preserved.
func TrackingClient(client llm.Client, record func(context.Context, Record)) llm.Client {
	if record == nil {
		return client
	}
	return &trackingClient{Client: client, record: record}
}

type trackingClient struct {
	llm.Client
	record func(context.Context, Record)
}

func (c *trackingClient) Chat(ctx context.Context, model string, messages []llm.Message, tools []map[string]any) (*llm.ChatResponse, error) {
	started := time.Now()
	response, err := c.Client.Chat(ctx, model, messages, tools)
	c.capture(ctx, model, response, started, err)
	return response, err
}

func (c *trackingClient) ChatStream(ctx context.Context, model string, messages []llm.Message, tools []map[string]any, callback llm.StreamCallback) (*llm.ChatResponse, error) {
	started := time.Now()
	response, err := c.Client.ChatStream(ctx, model, messages, tools, callback)
	c.capture(ctx, model, response, started, err)
	return response, err
}

func (c *trackingClient) capture(ctx context.Context, model string, response *llm.ChatResponse, started time.Time, err error) {
	if record, ok := CallRecord(model, response, started, err); ok {
		c.record(ctx, ApplyAttribution(record, llm.AttributionFromContext(ctx)))
	}
}
