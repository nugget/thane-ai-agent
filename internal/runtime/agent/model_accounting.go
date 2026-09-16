package agent

import (
	"context"
	"errors"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	"github.com/nugget/thane-ai-agent/internal/runtime/iterate"
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
	return c.accountCall(ctx, model, func(ctx context.Context) (*llm.ChatResponse, error) {
		return c.Client.Chat(ctx, model, messages, tools)
	})
}

func (c *accountingClient) ChatStream(ctx context.Context, model string, messages []llm.Message, tools []map[string]any, stream llm.StreamCallback) (*llm.ChatResponse, error) {
	return c.accountCall(ctx, model, func(ctx context.Context) (*llm.ChatResponse, error) {
		return c.Client.ChatStream(ctx, model, messages, tools, stream)
	})
}

func (c *accountingClient) accountCall(ctx context.Context, model string, call func(context.Context) (*llm.ChatResponse, error)) (*llm.ChatResponse, error) {
	if err := canceledContextError(c.accounting.observerContext, ctx); err != nil {
		return nil, err
	}
	if remaining, limited := c.accounting.remainingOutputTokens(); limited {
		if remaining == 0 {
			return nil, llm.ErrOutputBudgetExhausted
		}
		ctx = llm.WithMaxOutputTokens(ctx, llm.ClampMaxOutputTokens(ctx, remaining))
	}
	sessionID := c.accounting.fallbackSession
	if c.accounting.loop.archiver != nil {
		if active := c.accounting.loop.archiver.ActiveSessionID(c.accounting.conversationID); active != "" {
			sessionID = active
		}
	}
	attribution := llm.AttributionFromContext(c.accounting.observerContext)
	attribution.RequestID = c.accounting.requestID
	attribution.ConversationID = c.accounting.conversationID
	attribution.SessionID = sessionID
	ctx = llm.WithAttribution(ctx, attribution)
	started := time.Now()
	response, err := call(ctx)
	// A failed transport with no usage, or a synthetic fallback, is not
	// evidence of a billable model call. Do not invent a zero-token record.
	if record, ok := usage.CallRecord(firstNonEmpty(c.modelOverride, model), response, started, err); ok {
		a := c.accounting
		// A direct recovery client's response names the wire model; the
		// deployment override remains authoritative for pricing/provenance.
		if c.modelOverride != "" {
			record.Model = c.modelOverride
		}
		record = a.loop.makeUsageRecord(a.request, usage.ApplyAttribution(record, attribution))
		a.calls = append(a.calls, record)
		// Use the original run context: timeout recovery can use its own
		// generation deadline, but belongs to the same usage observer.
		usage.Observe(a.observerContext, record)
	}
	if err != nil && canceledContextError(c.accounting.observerContext, ctx) == nil && c.accounting.outputBudgetExhausted() {
		return response, errors.Join(llm.ErrOutputBudgetExhausted, err)
	}
	return response, err
}

// remainingOutputTokens derives the allowance from the same records used for
// billing, including failed attempts. Recovery clients share this accounting
// object, so detached contexts and direct provider calls cannot reset it.
// Missing provider usage is unknown and is never invented as a budget debit.
func (a *modelCallAccounting) remainingOutputTokens() (int, bool) {
	remaining := a.request.MaxOutputTokens
	if remaining <= 0 {
		return 0, false
	}
	for _, call := range a.calls {
		spent := max(0, call.OutputTokens)
		if spent >= remaining {
			return 0, true
		}
		remaining -= spent
	}
	return remaining, true
}

func (a *modelCallAccounting) outputBudgetExhausted() bool {
	remaining, limited := a.remainingOutputTokens()
	return limited && remaining == 0
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
