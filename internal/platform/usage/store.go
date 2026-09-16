// Package usage provides persistent token usage and cost tracking for
// LLM interactions. Records are append-only and indexed by timestamp,
// session, conversation, and loop for efficient aggregation queries.
package usage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// Record represents one model call's provider-reported token usage and cost.
// A request can produce multiple records when it iterates, retries, or changes
// models. Failed requests retain any usage already reported by the provider;
// a call without reported usage does not imply a zero-cost successful call.
type Record struct {
	ID             string    `json:"id"`
	Timestamp      time.Time `json:"timestamp"`
	RequestID      string    `json:"request_id"`
	SessionID      string    `json:"session_id"`
	ConversationID string    `json:"conversation_id"`
	LoopID         string    `json:"loop_id"`
	LoopName       string    `json:"loop_name"`
	ParentLoopID   string    `json:"parent_loop_id"`
	// Purpose identifies the operation that initiated the call, such as
	// agent execution or summarization. Empty means it was not captured.
	Purpose string `json:"purpose"`
	// UpstreamRequestID is the provider-side request identifier when
	// available (e.g. Anthropic's `x-request-id` response header).
	// Empty when the provider does not return one or the call failed
	// before headers arrived. Captured for support escalation and to
	// correlate our local r_* IDs with upstream invoice line items.
	UpstreamRequestID        string `json:"upstream_request_id"`
	Model                    string `json:"model"` // Selected deployment ID when known
	UpstreamModel            string `json:"upstream_model"`
	Resource                 string `json:"resource"`
	Provider                 string `json:"provider"` // Provider family, e.g. "anthropic", "ollama", "lmstudio"
	InputTokens              int    `json:"input_tokens"`
	OutputTokens             int    `json:"output_tokens"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens"`
	// CacheCreation5mInputTokens and CacheCreation1hInputTokens break
	// down the cache-write bucket by TTL when the provider exposes it
	// (Anthropic). Their sum is ≤ CacheCreationInputTokens; any
	// shortfall reflects writes the provider didn't attribute. When
	// the breakdown is absent (both zero), cost computation treats the
	// full CacheCreationInputTokens as 5m for the conservative default.
	CacheCreation5mInputTokens int     `json:"cache_creation_5m_input_tokens"`
	CacheCreation1hInputTokens int     `json:"cache_creation_1h_input_tokens"`
	CacheReadInputTokens       int     `json:"cache_read_input_tokens"`
	CostUSD                    float64 `json:"cost_usd"`
	Role                       string  `json:"role"`      // "interactive", "delegate", "scheduled", "autonomous", "auxiliary"
	TaskName                   string  `json:"task_name"` // "email_poll", "periodic_reflection", etc. (empty for interactive)
	// PricingStatus is "priced" when configured rates were applied, including
	// explicit zero rates, or "unpriced" when no rate was available. Empty
	// means unknown for legacy records; CostUSD retains its recorded value.
	PricingStatus string `json:"pricing_status"`
	// Outcome is "success", "error", or "canceled". Empty means unknown for
	// legacy records. Token counters only include provider-reported usage.
	Outcome string `json:"outcome"`
	// DurationMS is elapsed model-call time in milliseconds. Zero can also
	// mean the duration was not captured by a legacy writer.
	DurationMS int64 `json:"duration_ms"`
}

// ModelIdentity is the normalized usage-facing identity for a selected
// model/deployment.
type ModelIdentity struct {
	Model         string `json:"model"`
	UpstreamModel string `json:"upstream_model"`
	Resource      string `json:"resource"`
	Provider      string `json:"provider"`
}

// Summary holds aggregated token usage and cost totals. TotalRecords counts
// usage records, not distinct logical requests. New agent records represent
// individual model calls; older records can aggregate several iterations.
// Pricing coverage counts distinguish configured zero-cost calls from missing
// prices and unknown records (legacy or unrecognized pricing status).
// TotalCostUSD sums the costs recorded at call time.
type Summary struct {
	TotalRecords                  int     `json:"total_records"`
	TotalInputTokens              int64   `json:"total_input_tokens"`
	TotalOutputTokens             int64   `json:"total_output_tokens"`
	TotalCacheCreationInputTokens int64   `json:"total_cache_creation_input_tokens"`
	TotalCacheReadInputTokens     int64   `json:"total_cache_read_input_tokens"`
	TotalCostUSD                  float64 `json:"total_cost_usd"`
	PricedRecords                 int     `json:"priced_records"`
	UnpricedRecords               int     `json:"unpriced_records"`
	UnknownPricingRecords         int     `json:"unknown_pricing_records"`
}

// CacheHitRate returns the fraction of cache-eligible input tokens that
// were served from cache in this summary, as a value in [0, 1]. Zero
// when there were no cache-eligible tokens at all (empty window, or
// caching disabled). Useful for spotting cold-session spikes and
// validating that prompt-caching policy is actually working.
//
// Formula matches the Anthropic-recommended observability metric:
// cache_read / (cache_read + cache_creation).
func (s Summary) CacheHitRate() float64 {
	return llm.CacheHitRate(int(s.TotalCacheReadInputTokens), int(s.TotalCacheCreationInputTokens))
}

// GroupedSummary pairs a grouping key (model name, role, task name)
// with its aggregated usage totals. Slices of GroupedSummary preserve
// the SQL ordering (highest cost first).
type GroupedSummary struct {
	Key     string  `json:"key"`
	Summary Summary `json:"summary"`
}

// Store is an append-only SQLite store for token usage records. All
// public methods are safe for concurrent use (SQLite serializes writes).
type Store struct {
	db *sql.DB
}

// NewStore creates a usage store using the given database connection.
// The caller owns the connection — Store does not close it. The schema
// is created automatically on first use.
func NewStore(db *sql.DB, logger *slog.Logger) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("nil database connection")
	}

	if err := database.Migrate(db, schema, logger); err != nil {
		return nil, err
	}

	return &Store{db: db}, nil
}

// Record persists a usage record. If rec.ID is empty, a UUIDv7 is
// generated. The context is used for cancellation only.
func (s *Store) Record(ctx context.Context, rec Record) error {
	if rec.ID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate usage record ID: %w", err)
		}
		rec.ID = id.String()
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now()
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage_records
			(id, timestamp, request_id, upstream_request_id, session_id, conversation_id, model, upstream_model, resource, provider,
			 input_tokens, output_tokens, cache_creation_input_tokens, cache_creation_5m_input_tokens,
			 cache_creation_1h_input_tokens, cache_read_input_tokens, cost_usd, role, task_name,
			 loop_id, loop_name, parent_loop_id, purpose, pricing_status, outcome, duration_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID,
		rec.Timestamp.UTC().Format(time.RFC3339),
		rec.RequestID,
		rec.UpstreamRequestID,
		rec.SessionID,
		rec.ConversationID,
		rec.Model,
		rec.UpstreamModel,
		rec.Resource,
		rec.Provider,
		rec.InputTokens,
		rec.OutputTokens,
		rec.CacheCreationInputTokens,
		rec.CacheCreation5mInputTokens,
		rec.CacheCreation1hInputTokens,
		rec.CacheReadInputTokens,
		rec.CostUSD,
		rec.Role,
		rec.TaskName,
		rec.LoopID,
		rec.LoopName,
		rec.ParentLoopID,
		rec.Purpose,
		rec.PricingStatus,
		rec.Outcome,
		rec.DurationMS,
	)
	if err != nil {
		return fmt.Errorf("insert usage record: %w", err)
	}
	return nil
}

// Summary returns aggregated totals for records within [start, end).
func (s *Store) Summary(start, end time.Time) (*Summary, error) {
	return s.SummaryContext(context.Background(), start, end)
}

// SummaryContext is Summary honoring ctx cancellation — background
// collectors on a bounded budget use this so a wedged query cannot
// outlive its caller's deadline.
func (s *Store) SummaryContext(ctx context.Context, start, end time.Time) (*Summary, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_creation_input_tokens), 0), COALESCE(SUM(cache_read_input_tokens), 0),
		        COALESCE(SUM(cost_usd), 0),
		        COUNT(CASE WHEN pricing_status = 'priced' THEN 1 END),
		        COUNT(CASE WHEN pricing_status = 'unpriced' THEN 1 END),
		        COUNT(CASE WHEN pricing_status NOT IN ('priced', 'unpriced') THEN 1 END)
		 FROM usage_records
		 WHERE timestamp >= ? AND timestamp < ?`,
		start.UTC().Format(time.RFC3339),
		end.UTC().Format(time.RFC3339),
	)

	var sum Summary
	if err := row.Scan(&sum.TotalRecords, &sum.TotalInputTokens, &sum.TotalOutputTokens, &sum.TotalCacheCreationInputTokens, &sum.TotalCacheReadInputTokens, &sum.TotalCostUSD, &sum.PricedRecords, &sum.UnpricedRecords, &sum.UnknownPricingRecords); err != nil {
		return nil, fmt.Errorf("query usage summary: %w", err)
	}
	return &sum, nil
}

// SummaryByModel returns per-model aggregated totals for records within
// [start, end), ordered by cost descending.
func (s *Store) SummaryByModel(start, end time.Time) ([]GroupedSummary, error) {
	return s.summaryGroupedBy(context.Background(), "model", start, end)
}

// SummaryByModelContext is SummaryByModel honoring ctx cancellation;
// see SummaryContext.
func (s *Store) SummaryByModelContext(ctx context.Context, start, end time.Time) ([]GroupedSummary, error) {
	return s.summaryGroupedBy(ctx, "model", start, end)
}

// SummaryByUpstreamModel returns per-upstream-model aggregated totals
// for records within [start, end), ordered by cost descending.
func (s *Store) SummaryByUpstreamModel(start, end time.Time) ([]GroupedSummary, error) {
	return s.summaryGroupedBy(context.Background(), "upstream_model", start, end)
}

// SummaryByProvider returns per-provider aggregated totals for records
// within [start, end), ordered by cost descending.
func (s *Store) SummaryByProvider(start, end time.Time) ([]GroupedSummary, error) {
	return s.summaryGroupedBy(context.Background(), "provider", start, end)
}

// SummaryByResource returns per-resource aggregated totals for records
// within [start, end), ordered by cost descending.
func (s *Store) SummaryByResource(start, end time.Time) ([]GroupedSummary, error) {
	return s.summaryGroupedBy(context.Background(), "resource", start, end)
}

// SummaryByRole returns per-role aggregated totals for records within
// [start, end), ordered by cost descending.
func (s *Store) SummaryByRole(start, end time.Time) ([]GroupedSummary, error) {
	return s.summaryGroupedBy(context.Background(), "role", start, end)
}

// SummaryByTask returns per-task aggregated totals for records within
// [start, end), ordered by cost descending. Records with empty
// task_name are grouped under the key "".
func (s *Store) SummaryByTask(start, end time.Time) ([]GroupedSummary, error) {
	return s.summaryGroupedBy(context.Background(), "task_name", start, end)
}

// SummaryByGroup dispatches the grouped summary query based on the
// caller-provided grouping key.
func (s *Store) SummaryByGroup(groupBy string, start, end time.Time) ([]GroupedSummary, error) {
	switch strings.TrimSpace(groupBy) {
	case "deployment", "model":
		return s.SummaryByModel(start, end)
	case "upstream_model":
		return s.SummaryByUpstreamModel(start, end)
	case "provider":
		return s.SummaryByProvider(start, end)
	case "resource":
		return s.SummaryByResource(start, end)
	case "role":
		return s.SummaryByRole(start, end)
	case "task":
		return s.SummaryByTask(start, end)
	default:
		return nil, fmt.Errorf("unsupported group_by %q; use one of [\"deployment\" \"model\" \"upstream_model\" \"provider\" \"resource\" \"role\" \"task\"]", groupBy)
	}
}

func (s *Store) summaryGroupedBy(ctx context.Context, column string, start, end time.Time) ([]GroupedSummary, error) {
	// column is always a compile-time constant from our own methods,
	// never user input, so embedding it directly is safe.
	query := fmt.Sprintf(
		`SELECT COALESCE(%s, ''), COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_creation_input_tokens), 0), COALESCE(SUM(cache_read_input_tokens), 0), COALESCE(SUM(cost_usd), 0),
		        COUNT(CASE WHEN pricing_status = 'priced' THEN 1 END),
		        COUNT(CASE WHEN pricing_status = 'unpriced' THEN 1 END),
		        COUNT(CASE WHEN pricing_status NOT IN ('priced', 'unpriced') THEN 1 END)
		 FROM usage_records
		 WHERE timestamp >= ? AND timestamp < ?
		 GROUP BY %s
		 ORDER BY SUM(cost_usd) DESC`,
		column, column,
	)

	rows, err := s.db.QueryContext(ctx, query,
		start.UTC().Format(time.RFC3339),
		end.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("query usage by %s: %w", column, err)
	}
	defer rows.Close()

	var result []GroupedSummary
	for rows.Next() {
		var gs GroupedSummary
		if err := rows.Scan(&gs.Key, &gs.Summary.TotalRecords, &gs.Summary.TotalInputTokens, &gs.Summary.TotalOutputTokens, &gs.Summary.TotalCacheCreationInputTokens, &gs.Summary.TotalCacheReadInputTokens, &gs.Summary.TotalCostUSD, &gs.Summary.PricedRecords, &gs.Summary.UnpricedRecords, &gs.Summary.UnknownPricingRecords); err != nil {
			return nil, fmt.Errorf("scan usage by %s: %w", column, err)
		}
		result = append(result, gs)
	}
	return result, rows.Err()
}

// PriceRecord resolves rec's selected model identity and computes its cost using
// the supplied event-time rates, including cache-write TTLs. A configured zero
// rate is priced; absent rates yield zero cost and unpriced status. It returns a
// copy and preserves attribution, outcome, duration, and provider-reported token
// counters. Use it before persistence, never to reprice historical records.
func PriceRecord(rec Record, catalog *fleet.Catalog, pricing map[string]config.PricingEntry) Record {
	identity := ResolveModelIdentity(rec.Model, catalog)
	rec.Model = identity.Model
	rec.UpstreamModel = identity.UpstreamModel
	rec.Resource = identity.Resource
	rec.Provider = identity.Provider
	rec.CostUSD = ComputeDetailedCostForIdentityWithTTL(identity, rec.InputTokens,
		rec.CacheCreationInputTokens, rec.CacheCreation5mInputTokens,
		rec.CacheCreation1hInputTokens, rec.CacheReadInputTokens, rec.OutputTokens, pricing)
	rec.PricingStatus = "unpriced"
	if _, ok := PricingFor(identity, pricing); ok {
		rec.PricingStatus = "priced"
	}
	return rec
}

// ResolveModelIdentity resolves usage-facing metadata for a selected
// model/deployment. When a normalized catalog is available, it is used
// as the source of truth. Otherwise the function falls back to parsing
// deployment-qualified IDs like "resource/model".
func ResolveModelIdentity(model string, cat *fleet.Catalog) ModelIdentity {
	model = strings.TrimSpace(model)
	if cat != nil {
		if dep, ok := cat.DeploymentByRef(model); ok {
			return ModelIdentity{
				Model:         dep.ID,
				UpstreamModel: dep.ModelName,
				Resource:      dep.ResourceID,
				Provider:      dep.Provider,
			}
		}
	}

	identity := ModelIdentity{
		Model: model,
	}
	if slash := strings.Index(model, "/"); slash > 0 && slash < len(model)-1 {
		identity.Resource = model[:slash]
		identity.UpstreamModel = model[slash+1:]
	} else {
		identity.UpstreamModel = model
	}
	identity.Provider = ResolveProvider(identity.UpstreamModel)
	return identity
}

// ResolveProvider infers the LLM provider from the model name. Models
// starting with "claude-" are Anthropic; everything else is assumed to
// be Ollama (local).
func ResolveProvider(model string) string {
	model = strings.TrimSpace(model)
	if slash := strings.Index(model, "/"); slash > 0 && slash < len(model)-1 {
		model = model[slash+1:]
	}
	if strings.HasPrefix(model, "claude-") {
		return "anthropic"
	}
	return "ollama"
}

const (
	// Anthropic cache-write multipliers per TTL bucket (docs:
	// platform.claude.com). 5m is the default; 1h is an opt-in that
	// costs more to write in exchange for a longer hot window.
	anthropicCacheWrite5mMultiplier = 1.25
	anthropicCacheWrite1hMultiplier = 2.00
	// Alias retained for legacy callers that haven't been updated to
	// supply a TTL breakdown. Matches the 5m rate — the conservative
	// default when the provider doesn't attribute the writes.
	anthropicCacheWriteMultiplier = anthropicCacheWrite5mMultiplier
	anthropicCacheReadMultiplier  = 0.10
)

// ComputeDetailedCostForIdentity calculates USD cost for a resolved model
// identity using uncached input tokens, cache-write input tokens,
// cache-read input tokens, and output tokens. Deployment-qualified IDs
// fall back to upstream-model pricing when needed.
//
// Cache-write tokens are charged at the 5m rate (1.25× input) by
// default. Callers that know the per-TTL split should use
// [ComputeDetailedCostForIdentityWithTTL] instead to correctly charge
// 1h writes at 2.0×.
func ComputeDetailedCostForIdentity(identity ModelIdentity, inputTokens, cacheCreationInputTokens, cacheReadInputTokens, outputTokens int, pricing map[string]config.PricingEntry) float64 {
	return ComputeDetailedCostForIdentityWithTTL(identity, inputTokens, cacheCreationInputTokens, 0, 0, cacheReadInputTokens, outputTokens, pricing)
}

// ComputeDetailedCostForIdentityWithTTL is the full-fidelity cost
// function: callers supply the 5m/1h breakdown when the provider
// exposes it. The unattributed portion of cacheCreationInputTokens
// (that is, tokens not accounted for in the 5m or 1h buckets) is
// charged at the 5m rate to avoid retroactive price spikes on legacy
// records.
func ComputeDetailedCostForIdentityWithTTL(identity ModelIdentity, inputTokens, cacheCreationTotal, cacheCreation5m, cacheCreation1h, cacheReadInputTokens, outputTokens int, pricing map[string]config.PricingEntry) float64 {
	entry, ok := PricingFor(identity, pricing)
	if !ok {
		return 0
	}
	cost := float64(inputTokens) / 1_000_000.0 * entry.InputPerMillion

	// Breakdown-aware cache-write pricing. Anything in
	// cacheCreationTotal not attributed to 5m or 1h is charged at
	// the 5m rate (conservative default, matches legacy records
	// written before the breakdown columns existed).
	attributed := cacheCreation5m + cacheCreation1h
	unattributed := cacheCreationTotal - attributed
	if unattributed < 0 {
		unattributed = 0
	}
	cost += float64(cacheCreation5m+unattributed) / 1_000_000.0 * (entry.InputPerMillion * anthropicCacheWrite5mMultiplier)
	cost += float64(cacheCreation1h) / 1_000_000.0 * (entry.InputPerMillion * anthropicCacheWrite1hMultiplier)

	cost += float64(cacheReadInputTokens) / 1_000_000.0 * (entry.InputPerMillion * anthropicCacheReadMultiplier)
	cost += float64(outputTokens) / 1_000_000.0 * entry.OutputPerMillion
	return cost
}

// PricingFor returns the pricing entry for identity: the
// deployment-qualified model first, then the upstream model. ok is
// false when neither is in the table. Cost functions return zero when pricing
// is missing; that is an incomplete estimate, not evidence of free usage.
// An explicit zero-rate entry establishes known $0 configured API cost,
// including for local models, without implying zero resource consumption.
func PricingFor(identity ModelIdentity, pricing map[string]config.PricingEntry) (config.PricingEntry, bool) {
	if entry, ok := pricing[identity.Model]; ok {
		return entry, true
	}
	if identity.UpstreamModel != "" && identity.UpstreamModel != identity.Model {
		if entry, ok := pricing[identity.UpstreamModel]; ok {
			return entry, true
		}
	}
	return config.PricingEntry{}, false
}

// ComputeCostForIdentity calculates USD cost for a resolved model
// identity. The selected deployment ID is checked first, then the
// upstream model as a fallback so deployment-qualified IDs can reuse
// provider pricing entries keyed by upstream model name.
func ComputeCostForIdentity(identity ModelIdentity, inputTokens, outputTokens int, pricing map[string]config.PricingEntry) float64 {
	return ComputeDetailedCostForIdentity(identity, inputTokens, 0, 0, outputTokens, pricing)
}

// ComputeCost calculates the USD cost for a model's token usage based
// on the pricing table. Models not in the table return zero with unknown cost;
// callers must use [PricingFor] to distinguish missing rates from an explicit
// zero-rate entry.
func ComputeCost(model string, inputTokens, outputTokens int, pricing map[string]config.PricingEntry) float64 {
	return ComputeCostForIdentity(ResolveModelIdentity(model, nil), inputTokens, outputTokens, pricing)
}
