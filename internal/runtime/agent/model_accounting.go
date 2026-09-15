package agent

import (
	"context"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	"github.com/nugget/thane-ai-agent/internal/runtime/iterate"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// Model calls and iterations are different units: a retry can consume tokens
// before the same iteration succeeds on another deployment. Keep accounting
// at the client boundary so recovery cannot hide or reprice that work.
type modelCallAccounting struct {
	loop            *Loop
	conversationID  string
	fallbackSession string
	request         *Request
	requestID       string
	observerContext context.Context
	calls           []usage.Record
}

type accountingClient struct {
	llm.Client
	accounting    *modelCallAccounting
	modelOverride string
}

func (c *accountingClient) Chat(ctx context.Context, model string, messages []llm.Message, tools []map[string]any) (*llm.ChatResponse, error) {
	return c.accountCall(model, func() (*llm.ChatResponse, error) {
		return c.Client.Chat(ctx, model, messages, tools)
	})
}

func (c *accountingClient) ChatStream(ctx context.Context, model string, messages []llm.Message, tools []map[string]any, stream llm.StreamCallback) (*llm.ChatResponse, error) {
	return c.accountCall(model, func() (*llm.ChatResponse, error) {
		return c.Client.ChatStream(ctx, model, messages, tools, stream)
	})
}

func (c *accountingClient) accountCall(model string, call func() (*llm.ChatResponse, error)) (*llm.ChatResponse, error) {
	sessionID := c.accounting.fallbackSession
	if c.accounting.loop.archiver != nil {
		if active := c.accounting.loop.archiver.ActiveSessionID(c.accounting.conversationID); active != "" {
			sessionID = active
		}
	}
	started := time.Now()
	response, err := call()
	// A failed transport with no usage, or a synthetic fallback, is not
	// evidence of a billable model call. Do not invent a zero-token record.
	if response != nil && reportedUsage(response) {
		a := c.accounting
		record := a.loop.makeUsageRecord(a.request, usage.Record{
			Timestamp: started, Model: firstNonEmpty(c.modelOverride, response.Model, model),
			SessionID: memory.ShortID(sessionID), ConversationID: a.conversationID,
			RequestID: a.requestID, UpstreamRequestID: response.UpstreamRequestID,
			InputTokens: response.InputTokens, OutputTokens: response.OutputTokens,
			CacheCreationInputTokens:   response.CacheCreationInputTokens,
			CacheCreation5mInputTokens: response.CacheCreation5mInputTokens,
			CacheCreation1hInputTokens: response.CacheCreation1hInputTokens,
			CacheReadInputTokens:       response.CacheReadInputTokens,
		})
		a.calls = append(a.calls, record)
		// Use the original run context: timeout recovery can use its own
		// generation deadline, but belongs to the same usage observer.
		usage.Observe(a.observerContext, record)
	}
	return response, err
}

func reportedUsage(response *llm.ChatResponse) bool {
	return response.InputTokens != 0 || response.OutputTokens != 0 ||
		response.CacheCreationInputTokens != 0 || response.CacheReadInputTokens != 0 ||
		response.CacheCreation5mInputTokens != 0 || response.CacheCreation1hInputTokens != 0
}

// accountRecoveryClient preserves the same observer when explicit context
// recovery addresses a loaded runner instance directly. Its wire name is
// not the deployment identity used for pricing and provenance.
func accountRecoveryClient(client, direct llm.Client, deployment string) llm.Client {
	if observed, ok := client.(*accountingClient); ok {
		return &accountingClient{Client: direct, accounting: observed.accounting, modelOverride: deployment}
	}
	return direct
}

func (a *modelCallAccounting) persist(ctx context.Context) {
	if a.loop.usageStore == nil || len(a.calls) == 0 {
		return
	}
	// Already-incurred usage must survive caller cancellation. Only the
	// bounded local write is detached; no new generation runs here.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, call := range a.calls {
		a.loop.recordUsage(writeCtx, call)
	}
}

func (a *modelCallAccounting) applyTotals(result *iterate.Result) {
	if result == nil {
		return
	}
	result.InputTokens, result.OutputTokens, result.PeakInputTokens = 0, 0, 0
	result.CacheCreationInputTokens, result.CacheCreation5mInputTokens, result.CacheCreation1hInputTokens, result.CacheReadInputTokens = 0, 0, 0, 0
	for _, r := range a.calls {
		result.InputTokens += r.InputTokens
		result.OutputTokens += r.OutputTokens
		result.PeakInputTokens = max(result.PeakInputTokens, r.InputTokens)
		result.CacheCreationInputTokens += r.CacheCreationInputTokens
		result.CacheCreation5mInputTokens += r.CacheCreation5mInputTokens
		result.CacheCreation1hInputTokens += r.CacheCreation1hInputTokens
		result.CacheReadInputTokens += r.CacheReadInputTokens
	}
}
