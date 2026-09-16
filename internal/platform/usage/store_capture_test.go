package usage

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func TestPriceRecord(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Models.Resources = map[string]config.ModelServerConfig{
		"edge":   {URL: "http://edge.example:11434", Provider: "openai_compat"},
		"backup": {URL: "http://backup.example:11434", Provider: "openai_compat"},
	}
	cfg.Models.Available = []config.ModelConfig{
		{Name: "example-model", Resource: "edge", SupportsTools: true, ContextWindow: 32768, Speed: 7, Quality: 6},
		{Name: "example-model", Resource: "backup", SupportsTools: true, ContextWindow: 32768, Speed: 7, Quality: 6},
	}
	catalog, err := fleet.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	rec := Record{
		Model: "edge/example-model", InputTokens: 1_000_000, OutputTokens: 100_000,
		CacheCreationInputTokens: 1_000_000, CacheCreation5mInputTokens: 200_000,
		CacheCreation1hInputTokens: 300_000, CacheReadInputTokens: 1_000_000,
		RequestID: "request", SessionID: "session", ConversationID: "conversation",
		LoopID: "loop", LoopName: "reflection", ParentLoopID: "parent", Purpose: "agent",
		UpstreamRequestID: "provider-request", Outcome: "error", DurationMS: 123,
		CostUSD: 99, PricingStatus: "priced",
	}
	for _, tt := range []struct {
		name    string
		pricing map[string]config.PricingEntry
		status  string
		cost    float64
	}{
		{name: "missing pricing", status: "unpriced"},
		{
			name: "configured zero overrides upstream price", status: "priced",
			pricing: map[string]config.PricingEntry{
				"edge/example-model": {},
				"example-model":      {InputPerMillion: 3, OutputPerMillion: 15},
			},
		},
		{
			name: "upstream fallback retains cache TTL pricing", status: "priced", cost: 9.225,
			pricing: map[string]config.PricingEntry{"example-model": {InputPerMillion: 3, OutputPerMillion: 15}},
		},
		{
			name: "deployment price takes precedence", status: "priced", cost: 3.075,
			pricing: map[string]config.PricingEntry{
				"edge/example-model": {InputPerMillion: 1, OutputPerMillion: 5},
				"example-model":      {InputPerMillion: 3, OutputPerMillion: 15},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := PriceRecord(rec, catalog, tt.pricing)
			want := rec
			want.UpstreamModel = "example-model"
			want.Resource = "edge"
			want.Provider = "openai_compat"
			want.PricingStatus = tt.status
			want.CostUSD = got.CostUSD
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("priced record = %+v, want %+v", got, want)
			}
			if math.Abs(got.CostUSD-tt.cost) > 1e-9 {
				t.Errorf("cost = %v, want %v", got.CostUSD, tt.cost)
			}
			if rec.CostUSD != 99 || rec.PricingStatus != "priced" || rec.Provider != "" {
				t.Fatal("PriceRecord modified its input")
			}
		})
	}

	t.Run("catalog unavailable", func(t *testing.T) {
		got := PriceRecord(Record{Model: "mirror/claude-example", InputTokens: 1_000_000}, nil,
			map[string]config.PricingEntry{"claude-example": {InputPerMillion: 3}})
		if got.Model != "mirror/claude-example" || got.Resource != "mirror" || got.UpstreamModel != "claude-example" || got.Provider != "anthropic" || got.CostUSD != 3 || got.PricingStatus != "priced" {
			t.Fatalf("fallback identity/pricing = %+v", got)
		}
	})
}

func TestRecordPersistsLoopCapture(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	for _, outcome := range []string{"success", "error", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			rec := Record{
				ID: outcome, Model: "edge/model", LoopID: "loop-1", LoopName: "review",
				ParentLoopID: "parent-1", Purpose: "summarize", PricingStatus: "unpriced",
				Outcome: outcome, DurationMS: 2345,
			}
			if err := s.Record(context.Background(), rec); err != nil {
				t.Fatalf("record: %v", err)
			}
			got := Record{ID: rec.ID, Model: rec.Model}
			if err := s.db.QueryRow(`SELECT loop_id, loop_name, parent_loop_id, purpose, pricing_status, outcome, duration_ms
				FROM usage_records WHERE id = ?`, rec.ID).Scan(&got.LoopID, &got.LoopName, &got.ParentLoopID, &got.Purpose, &got.PricingStatus, &got.Outcome, &got.DurationMS); err != nil {
				t.Fatalf("read recorded attribution: %v", err)
			}
			if got != rec {
				t.Fatalf("persisted capture = %+v, want %+v", got, rec)
			}
		})
	}
}

func TestSummaryPricingCoverage(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	for i, rec := range []Record{
		{PricingStatus: "priced", CostUSD: 2.5},
		{PricingStatus: "priced"}, // Explicitly free is covered, not missing.
		{PricingStatus: "unpriced"},
		{CostUSD: 1.25}, // Legacy costs are retained without inferring coverage.
		{PricingStatus: "future-status", CostUSD: 0.25},
	} {
		rec.Timestamp = now
		rec.Model, rec.UpstreamModel, rec.Provider, rec.Resource = "deployment", "model", "provider", "resource"
		rec.Role, rec.TaskName = "role", "task"
		rec.InputTokens, rec.OutputTokens = i+1, 10
		if err := s.Record(context.Background(), rec); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	if err := s.Record(context.Background(), Record{Timestamp: now.Add(time.Hour), PricingStatus: "unpriced", CostUSD: 100}); err != nil {
		t.Fatalf("record outside window: %v", err)
	}
	want := Summary{TotalRecords: 5, TotalInputTokens: 15, TotalOutputTokens: 50, TotalCostUSD: 4,
		PricedRecords: 2, UnpricedRecords: 1, UnknownPricingRecords: 2}
	start, end := now.Add(-time.Minute), now.Add(time.Minute)
	got, err := s.SummaryContext(context.Background(), start, end)
	if err != nil || got == nil || *got != want {
		t.Fatalf("summary = %+v, %v; want %+v", got, err, want)
	}
	for _, group := range []string{"deployment", "model", "upstream_model", "provider", "resource", "role", "task"} {
		t.Run(group, func(t *testing.T) {
			groups, err := s.SummaryByGroup(group, start, end)
			if err != nil || len(groups) != 1 || groups[0].Summary != want {
				t.Fatalf("grouped summary = %+v, %v; want %+v", groups, err, want)
			}
		})
	}
	got, err = s.Summary(end, now.Add(2*time.Minute))
	if err != nil || got == nil || *got != (Summary{}) {
		t.Fatalf("empty summary = %+v, %v", got, err)
	}
}

func TestCaptureMigrationPreservesLegacyCosts(t *testing.T) {
	t.Parallel()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The original usage schema has no loop, outcome, or pricing-coverage
	// columns. Both paid and zero-cost historical rows must remain unknown.
	if _, err := db.Exec(`CREATE TABLE usage_records (
		id TEXT PRIMARY KEY, timestamp TEXT NOT NULL, request_id TEXT NOT NULL,
		session_id TEXT, conversation_id TEXT, model TEXT NOT NULL, provider TEXT NOT NULL,
		input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL,
		cost_usd REAL NOT NULL, role TEXT NOT NULL, task_name TEXT);
		INSERT INTO usage_records (id, timestamp, request_id, model, provider, input_tokens, output_tokens, cost_usd, role)
		VALUES ('paid', '2026-09-16T12:00:00Z', 'paid', 'model', 'provider', 100, 50, 1.25, 'interactive'),
		       ('zero', '2026-09-16T12:00:00Z', 'zero', 'model', 'provider', 200, 75, 0, 'interactive')`); err != nil {
		t.Fatalf("create legacy records: %v", err)
	}
	for range 2 {
		s, err := NewStore(db, nil)
		if err != nil {
			t.Fatalf("migrate: %v", err)
		}
		start := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
		got, err := s.Summary(start, start.Add(24*time.Hour))
		want := Summary{TotalRecords: 2, TotalInputTokens: 300, TotalOutputTokens: 125, TotalCostUSD: 1.25, UnknownPricingRecords: 2}
		if err != nil || got == nil || *got != want {
			t.Fatalf("migrated summary = %+v, %v; want %+v", got, err, want)
		}
		var unknown int
		if err := db.QueryRow(`SELECT COUNT(*) FROM usage_records WHERE loop_id = '' AND loop_name = ''
			AND parent_loop_id = '' AND purpose = '' AND pricing_status = '' AND outcome = '' AND duration_ms = 0`).Scan(&unknown); err != nil {
			t.Fatal(err)
		}
		if unknown != 2 {
			t.Fatalf("legacy records with unknown capture = %d, want 2", unknown)
		}
	}
}
