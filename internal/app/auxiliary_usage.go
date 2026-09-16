package app

import (
	"context"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
)

// Auxiliary clients are constructed before the ledger in initStores, but only
// used after initialization. Resolve dependencies when a call completes so
// these clients also price against the latest effective model catalog.
func (a *App) auxiliaryUsageClient(purpose string) llm.Client {
	return usage.TrackingClient(a.llmClient, func(ctx context.Context, record usage.Record) {
		catalog := a.modelCatalog
		if a.modelRegistry != nil {
			catalog = a.modelRegistry.Catalog()
		}
		record = usage.PriceRecord(record, catalog, a.cfg.Pricing)
		record.Role = "auxiliary"
		record.Purpose = purpose
		if a.usageStore == nil {
			a.logger.Warn("auxiliary usage recording unavailable", "purpose", purpose, "model", record.Model)
			return
		}

		// Usage has already been incurred, including when the provider returns
		// an error or cancellation. Bound this write independently of that call.
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := a.usageStore.Record(writeCtx, record); err != nil {
			a.logger.Warn("failed to record auxiliary usage",
				"purpose", purpose, "model", record.Model,
				"conversation_id", record.ConversationID, "session_id", record.SessionID,
				"loop_id", record.LoopID, "error", err)
		}
	})
}
