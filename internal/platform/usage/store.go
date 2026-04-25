// Package usage provides persistent token usage and cost tracking for
// LLM interactions. Records are append-only and indexed by timestamp,
// session, and conversation for efficient aggregation queries.
package usage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// Record represents a single LLM interaction's token usage and cost.
type Record struct {
	ID                       string
	Timestamp                time.Time
	RequestID                string
	SessionID                string
	ConversationID           string
	Model                    string // Selected deployment ID when known
	UpstreamModel            string
	Resource                 string
	Provider                 string // Provider family, e.g. "anthropic", "ollama", "lmstudio"
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	// CacheCreation5mInputTokens and CacheCreation1hInputTokens break
	// down the cache-write bucket by TTL when the provider exposes it
	// (Anthropic). Their sum is ≤ CacheCreationInputTokens; any
	// shortfall reflects writes the provider didn't attribute. When
	// the breakdown is absent (both zero), cost computation treats the
	// full CacheCreationInputTokens as 5m for the conservative default.
	CacheCreation5mInputTokens int
	CacheCreation1hInputTokens int
	CacheReadInputTokens       int
	CostUSD                    float64
	Role                       string // "interactive", "delegate", "scheduled", "auxiliary"
	TaskName                   string // "email_poll", "periodic_reflection", etc. (empty for interactive)
}

// ModelIdentity is the normalized usage-facing identity for a selected
// model/deployment.
type ModelIdentity struct {
	Model         string
	UpstreamModel string
	Resource      string
	Provider      string
}

// Summary holds aggregated token usage and cost totals.
type Summary struct {
	TotalRecords                  int     `json:"total_records"`
	TotalInputTokens              int64   `json:"total_input_tokens"`
	TotalOutputTokens             int64   `json:"total_output_tokens"`
	TotalCacheCreationInputTokens int64   `json:"total_cache_creation_input_tokens"`
	TotalCacheReadInputTokens     int64   `json:"total_cache_read_input_tokens"`
	TotalCostUSD                  float64 `json:"total_cost_usd"`
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
func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("nil database connection")
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate usage schema: %w", err)
	}

	return s, nil
}

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS usage_records (
		id              TEXT PRIMARY KEY,
		timestamp       TEXT NOT NULL,
		request_id      TEXT NOT NULL,
		session_id      TEXT,
		conversation_id TEXT,
		model           TEXT NOT NULL,
		provider        TEXT NOT NULL,
		input_tokens    INTEGER NOT NULL,
		output_tokens   INTEGER NOT NULL,
		cost_usd        REAL NOT NULL,
		role            TEXT NOT NULL,
		task_name       TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_usage_timestamp ON usage_records(timestamp);
	CREATE INDEX IF NOT EXISTS idx_usage_session ON usage_records(session_id);
	CREATE INDEX IF NOT EXISTS idx_usage_conversation ON usage_records(conversation_id);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	if err := database.AddColumn(s.db, "usage_records", "upstream_model", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := database.AddColumn(s.db, "usage_records", "resource", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := database.AddColumn(s.db, "usage_records", "cache_creation_input_tokens", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := database.AddColumn(s.db, "usage_records", "cache_read_input_tokens", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// Per-TTL cache-write breakdown columns, added for #736. Rows
	// written before this migration ran have NULL/0 in both buckets;
	// ComputeDetailedCostForIdentity treats that as "unknown mix" and
	// falls back to the 5m multiplier on the full total so historical
	// cost numbers don't spike retroactively.
	if err := database.AddColumn(s.db, "usage_records", "cache_creation_5m_input_tokens", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := database.AddColumn(s.db, "usage_records", "cache_creation_1h_input_tokens", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return nil
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
			(id, timestamp, request_id, session_id, conversation_id, model, upstream_model, resource, provider,
			 input_tokens, output_tokens, cache_creation_input_tokens, cache_creation_5m_input_tokens,
			 cache_creation_1h_input_tokens, cache_read_input_tokens, cost_usd, role, task_name)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID,
		rec.Timestamp.UTC().Format(time.RFC3339),
		rec.RequestID,
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
	)
	if err != nil {
		return fmt.Errorf("insert usage record: %w", err)
	}
	return nil
}

// Summary returns aggregated totals for records within [start, end).
func (s *Store) Summary(start, end time.Time) (*Summary, error) {
	row := s.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_creation_input_tokens), 0), COALESCE(SUM(cache_read_input_tokens), 0),
		        COALESCE(SUM(cost_usd), 0)
		 FROM usage_records
		 WHERE timestamp >= ? AND timestamp < ?`,
		start.UTC().Format(time.RFC3339),
		end.UTC().Format(time.RFC3339),
	)

	var sum Summary
	if err := row.Scan(&sum.TotalRecords, &sum.TotalInputTokens, &sum.TotalOutputTokens, &sum.TotalCacheCreationInputTokens, &sum.TotalCacheReadInputTokens, &sum.TotalCostUSD); err != nil {
		return nil, fmt.Errorf("query usage summary: %w", err)
	}
	return &sum, nil
}

// SummaryByModel returns per-model aggregated totals for records within
// [start, end), ordered by cost descending.
func (s *Store) SummaryByModel(start, end time.Time) ([]GroupedSummary, error) {
	return s.summaryGroupedBy("model", start, end)
}

// SummaryByUpstreamModel returns per-upstream-model aggregated totals
// for records within [start, end), ordered by cost descending.
func (s *Store) SummaryByUpstreamModel(start, end time.Time) ([]GroupedSummary, error) {
	return s.summaryGroupedBy("upstream_model", start, end)
}

// SummaryByProvider returns per-provider aggregated totals for records
// within [start, end), ordered by cost descending.
func (s *Store) SummaryByProvider(start, end time.Time) ([]GroupedSummary, error) {
	return s.summaryGroupedBy("provider", start, end)
}

// SummaryByResource returns per-resource aggregated totals for records
// within [start, end), ordered by cost descending.
func (s *Store) SummaryByResource(start, end time.Time) ([]GroupedSummary, error) {
	return s.summaryGroupedBy("resource", start, end)
}

// SummaryByRole returns per-role aggregated totals for records within
// [start, end), ordered by cost descending.
func (s *Store) SummaryByRole(start, end time.Time) ([]GroupedSummary, error) {
	return s.summaryGroupedBy("role", start, end)
}

// SummaryByTask returns per-task aggregated totals for records within
// [start, end), ordered by cost descending. Records with empty
// task_name are grouped under the key "".
func (s *Store) SummaryByTask(start, end time.Time) ([]GroupedSummary, error) {
	return s.summaryGroupedBy("task_name", start, end)
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

func (s *Store) summaryGroupedBy(column string, start, end time.Time) ([]GroupedSummary, error) {
	// column is always a compile-time constant from our own methods,
	// never user input, so embedding it directly is safe.
	query := fmt.Sprintf(
		`SELECT COALESCE(%s, ''), COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_creation_input_tokens), 0), COALESCE(SUM(cache_read_input_tokens), 0), COALESCE(SUM(cost_usd), 0)
		 FROM usage_records
		 WHERE timestamp >= ? AND timestamp < ?
		 GROUP BY %s
		 ORDER BY SUM(cost_usd) DESC`,
		column, column,
	)

	rows, err := s.db.Query(query,
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
		if err := rows.Scan(&gs.Key, &gs.Summary.TotalRecords, &gs.Summary.TotalInputTokens, &gs.Summary.TotalOutputTokens, &gs.Summary.TotalCacheCreationInputTokens, &gs.Summary.TotalCacheReadInputTokens, &gs.Summary.TotalCostUSD); err != nil {
			return nil, fmt.Errorf("scan usage by %s: %w", column, err)
		}
		result = append(result, gs)
	}
	return result, rows.Err()
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
	if len(pricing) == 0 {
		return 0
	}

	keys := []string{identity.Model}
	if identity.UpstreamModel != "" && identity.UpstreamModel != identity.Model {
		keys = append(keys, identity.UpstreamModel)
	}
	for _, key := range keys {
		entry, ok := pricing[key]
		if !ok {
			continue
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
	return 0
}

// ComputeCostForIdentity calculates USD cost for a resolved model
// identity. The selected deployment ID is checked first, then the
// upstream model as a fallback so deployment-qualified IDs can reuse
// provider pricing entries keyed by upstream model name.
func ComputeCostForIdentity(identity ModelIdentity, inputTokens, outputTokens int, pricing map[string]config.PricingEntry) float64 {
	return ComputeDetailedCostForIdentity(identity, inputTokens, 0, 0, outputTokens, pricing)
}

// ComputeCost calculates the USD cost for a model's token usage based
// on the pricing table. Models not in the table are treated as free
// (local/Ollama models).
func ComputeCost(model string, inputTokens, outputTokens int, pricing map[string]config.PricingEntry) float64 {
	return ComputeCostForIdentity(ResolveModelIdentity(model, nil), inputTokens, outputTokens, pricing)
}
