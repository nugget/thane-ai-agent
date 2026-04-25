package usage

import (
	"context"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// testPricing returns a pricing table for tests.
func testPricing() map[string]config.PricingEntry {
	return map[string]config.PricingEntry{
		"claude-opus-4-20250514":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
		"claude-sonnet-4-20250514": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
	}
}

func TestRecord_And_Summary(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	recs := []Record{
		{
			Timestamp:                now,
			RequestID:                "r_001",
			SessionID:                "sess-1",
			ConversationID:           "conv-1",
			Model:                    "claude-opus-4-20250514",
			Provider:                 "anthropic",
			InputTokens:              1000,
			OutputTokens:             500,
			CacheCreationInputTokens: 4000,
			CacheReadInputTokens:     8000,
			CostUSD:                  0.0525 + 0.075 + 0.012, // base + cache write + cache read
			Role:                     "interactive",
		},
		{
			Timestamp:      now,
			RequestID:      "r_002",
			SessionID:      "sess-1",
			ConversationID: "conv-1",
			Model:          "claude-sonnet-4-20250514",
			Provider:       "anthropic",
			InputTokens:    2000,
			OutputTokens:   1000,
			CostUSD:        0.021, // 2000/1M*3 + 1000/1M*15
			Role:           "delegate",
		},
	}

	for _, rec := range recs {
		if err := s.Record(ctx, rec); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	start := now.Add(-1 * time.Minute)
	end := now.Add(1 * time.Minute)
	sum, err := s.Summary(start, end)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}

	if sum.TotalRecords != 2 {
		t.Errorf("TotalRecords = %d, want 2", sum.TotalRecords)
	}
	if sum.TotalInputTokens != 3000 {
		t.Errorf("TotalInputTokens = %d, want 3000", sum.TotalInputTokens)
	}
	if sum.TotalOutputTokens != 1500 {
		t.Errorf("TotalOutputTokens = %d, want 1500", sum.TotalOutputTokens)
	}
	if sum.TotalCacheCreationInputTokens != 4000 {
		t.Errorf("TotalCacheCreationInputTokens = %d, want 4000", sum.TotalCacheCreationInputTokens)
	}
	if sum.TotalCacheReadInputTokens != 8000 {
		t.Errorf("TotalCacheReadInputTokens = %d, want 8000", sum.TotalCacheReadInputTokens)
	}
	// (0.0525 + 0.075 + 0.012) + 0.021 = 0.1605
	if diff := sum.TotalCostUSD - 0.1605; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("TotalCostUSD = %f, want ~0.1605", sum.TotalCostUSD)
	}
}

func TestSummaryByModel(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	recs := []Record{
		{Timestamp: now, RequestID: "r1", Model: "opus", Provider: "anthropic", InputTokens: 100, OutputTokens: 50, CostUSD: 1.0, Role: "interactive"},
		{Timestamp: now, RequestID: "r2", Model: "opus", Provider: "anthropic", InputTokens: 200, OutputTokens: 100, CostUSD: 2.0, Role: "interactive"},
		{Timestamp: now, RequestID: "r3", Model: "sonnet", Provider: "anthropic", InputTokens: 50, OutputTokens: 25, CostUSD: 0.5, Role: "delegate"},
	}
	for _, rec := range recs {
		if err := s.Record(ctx, rec); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	start := now.Add(-1 * time.Minute)
	end := now.Add(1 * time.Minute)
	result, err := s.SummaryByModel(start, end)
	if err != nil {
		t.Fatalf("SummaryByModel: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("got %d groups, want 2", len(result))
	}

	// Results are ordered by cost DESC, so opus (cost 3.0) comes first.
	if result[0].Key != "opus" {
		t.Errorf("first group key = %q, want %q", result[0].Key, "opus")
	}
	opus := result[0].Summary
	if opus.TotalRecords != 2 {
		t.Errorf("opus.TotalRecords = %d, want 2", opus.TotalRecords)
	}
	if opus.TotalInputTokens != 300 {
		t.Errorf("opus.TotalInputTokens = %d, want 300", opus.TotalInputTokens)
	}
	if opus.TotalCostUSD != 3.0 {
		t.Errorf("opus.TotalCostUSD = %f, want 3.0", opus.TotalCostUSD)
	}

	if result[1].Key != "sonnet" {
		t.Errorf("second group key = %q, want %q", result[1].Key, "sonnet")
	}
	if result[1].Summary.TotalRecords != 1 {
		t.Errorf("sonnet.TotalRecords = %d, want 1", result[1].Summary.TotalRecords)
	}
}

func TestSummaryByRole(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	recs := []Record{
		{Timestamp: now, RequestID: "r1", Model: "m", Provider: "p", InputTokens: 100, OutputTokens: 50, CostUSD: 1.0, Role: "interactive"},
		{Timestamp: now, RequestID: "r2", Model: "m", Provider: "p", InputTokens: 200, OutputTokens: 100, CostUSD: 2.0, Role: "delegate"},
		{Timestamp: now, RequestID: "r3", Model: "m", Provider: "p", InputTokens: 300, OutputTokens: 150, CostUSD: 3.0, Role: "scheduled"},
	}
	for _, rec := range recs {
		if err := s.Record(ctx, rec); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	start := now.Add(-1 * time.Minute)
	end := now.Add(1 * time.Minute)
	result, err := s.SummaryByRole(start, end)
	if err != nil {
		t.Fatalf("SummaryByRole: %v", err)
	}

	if len(result) != 3 {
		t.Fatalf("got %d groups, want 3", len(result))
	}

	// Ordered by cost DESC: scheduled (3.0), delegate (2.0), interactive (1.0).
	wantOrder := []string{"scheduled", "delegate", "interactive"}
	for i, want := range wantOrder {
		if result[i].Key != want {
			t.Errorf("result[%d].Key = %q, want %q", i, result[i].Key, want)
		}
	}

	if result[0].Summary.TotalCostUSD != 3.0 {
		t.Errorf("scheduled cost = %f, want 3.0", result[0].Summary.TotalCostUSD)
	}
}

func TestSummaryByTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	recs := []Record{
		{Timestamp: now, RequestID: "r1", Model: "m", Provider: "p", InputTokens: 100, OutputTokens: 50, CostUSD: 1.0, Role: "scheduled", TaskName: "email_poll"},
		{Timestamp: now, RequestID: "r2", Model: "m", Provider: "p", InputTokens: 200, OutputTokens: 100, CostUSD: 2.0, Role: "scheduled", TaskName: "email_poll"},
		{Timestamp: now, RequestID: "r3", Model: "m", Provider: "p", InputTokens: 300, OutputTokens: 150, CostUSD: 3.0, Role: "scheduled", TaskName: "periodic_reflection"},
		{Timestamp: now, RequestID: "r4", Model: "m", Provider: "p", InputTokens: 50, OutputTokens: 25, CostUSD: 0.5, Role: "interactive"},
	}
	for _, rec := range recs {
		if err := s.Record(ctx, rec); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	start := now.Add(-1 * time.Minute)
	end := now.Add(1 * time.Minute)
	result, err := s.SummaryByTask(start, end)
	if err != nil {
		t.Fatalf("SummaryByTask: %v", err)
	}

	if len(result) != 3 {
		t.Fatalf("got %d groups, want 3", len(result))
	}

	// Ordered by cost DESC: email_poll (3.0), periodic_reflection (3.0), "" (0.5).
	// Find email_poll group by key.
	var emailPoll *GroupedSummary
	var noTask *GroupedSummary
	for i := range result {
		switch result[i].Key {
		case "email_poll":
			emailPoll = &result[i]
		case "":
			noTask = &result[i]
		}
	}

	if emailPoll == nil {
		t.Fatal("missing 'email_poll' group")
	}
	if emailPoll.Summary.TotalRecords != 2 {
		t.Errorf("email_poll.TotalRecords = %d, want 2", emailPoll.Summary.TotalRecords)
	}
	if emailPoll.Summary.TotalCostUSD != 3.0 {
		t.Errorf("email_poll.TotalCostUSD = %f, want 3.0", emailPoll.Summary.TotalCostUSD)
	}

	// Records with no task_name are grouped under "".
	if noTask == nil {
		t.Fatal("missing empty-string task group")
	}
	if noTask.Summary.TotalRecords != 1 {
		t.Errorf("empty task TotalRecords = %d, want 1", noTask.Summary.TotalRecords)
	}
}

func TestQueryByPeriod_Filters(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	recs := []Record{
		{Timestamp: base.Add(-2 * time.Hour), RequestID: "old", Model: "m", Provider: "p", Role: "interactive", CostUSD: 1.0},
		{Timestamp: base, RequestID: "in-range", Model: "m", Provider: "p", Role: "interactive", CostUSD: 2.0},
		{Timestamp: base.Add(2 * time.Hour), RequestID: "future", Model: "m", Provider: "p", Role: "interactive", CostUSD: 3.0},
	}
	for _, rec := range recs {
		if err := s.Record(ctx, rec); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	// Only "in-range" should match.
	start := base.Add(-1 * time.Minute)
	end := base.Add(1 * time.Minute)
	sum, err := s.Summary(start, end)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum.TotalRecords != 1 {
		t.Errorf("TotalRecords = %d, want 1 (only in-range)", sum.TotalRecords)
	}
	if sum.TotalCostUSD != 2.0 {
		t.Errorf("TotalCostUSD = %f, want 2.0", sum.TotalCostUSD)
	}
}

func TestSummary_EmptyDB(t *testing.T) {
	s := testStore(t)

	start := time.Now().Add(-24 * time.Hour)
	end := time.Now().Add(24 * time.Hour)
	sum, err := s.Summary(start, end)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum == nil {
		t.Fatal("Summary returned nil, want non-nil zero-value Summary")
	}
	if sum.TotalRecords != 0 {
		t.Errorf("TotalRecords = %d, want 0", sum.TotalRecords)
	}
	if sum.TotalCostUSD != 0 {
		t.Errorf("TotalCostUSD = %f, want 0", sum.TotalCostUSD)
	}
}

func TestSummaryByModel_EmptyDB(t *testing.T) {
	s := testStore(t)

	start := time.Now().Add(-24 * time.Hour)
	end := time.Now().Add(24 * time.Hour)
	result, err := s.SummaryByModel(start, end)
	if err != nil {
		t.Fatalf("SummaryByModel: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("got %d groups, want 0", len(result))
	}
}

func TestSummaryByGroup_DeploymentDimensions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	recs := []Record{
		{
			Timestamp:     now,
			RequestID:     "r1",
			Model:         "mirror/gpt-oss:20b",
			UpstreamModel: "gpt-oss:20b",
			Resource:      "mirror",
			Provider:      "ollama",
			CostUSD:       2.0,
			Role:          "interactive",
		},
		{
			Timestamp:     now,
			RequestID:     "r2",
			Model:         "spark/gpt-oss:20b",
			UpstreamModel: "gpt-oss:20b",
			Resource:      "spark",
			Provider:      "ollama",
			CostUSD:       1.0,
			Role:          "interactive",
		},
		{
			Timestamp:     now,
			RequestID:     "r3",
			Model:         "anthropic/claude-sonnet-4-20250514",
			UpstreamModel: "claude-sonnet-4-20250514",
			Resource:      "anthropic",
			Provider:      "anthropic",
			CostUSD:       3.0,
			Role:          "delegate",
		},
	}
	for _, rec := range recs {
		if err := s.Record(ctx, rec); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	start := now.Add(-1 * time.Minute)
	end := now.Add(1 * time.Minute)

	upstream, err := s.SummaryByUpstreamModel(start, end)
	if err != nil {
		t.Fatalf("SummaryByUpstreamModel: %v", err)
	}
	if len(upstream) != 2 {
		t.Fatalf("upstream groups = %d, want 2", len(upstream))
	}
	if upstream[0].Key != "gpt-oss:20b" {
		t.Fatalf("first upstream key = %q, want %q", upstream[0].Key, "gpt-oss:20b")
	}
	if upstream[0].Summary.TotalRecords != 2 {
		t.Fatalf("gpt-oss:20b records = %d, want 2", upstream[0].Summary.TotalRecords)
	}

	resource, err := s.SummaryByResource(start, end)
	if err != nil {
		t.Fatalf("SummaryByResource: %v", err)
	}
	if len(resource) != 3 {
		t.Fatalf("resource groups = %d, want 3", len(resource))
	}
	if resource[0].Key != "anthropic" {
		t.Fatalf("first resource key = %q, want %q", resource[0].Key, "anthropic")
	}

	provider, err := s.SummaryByProvider(start, end)
	if err != nil {
		t.Fatalf("SummaryByProvider: %v", err)
	}
	if len(provider) != 2 {
		t.Fatalf("provider groups = %d, want 2", len(provider))
	}
	if provider[0].Key != "ollama" {
		t.Fatalf("first provider key = %q, want %q", provider[0].Key, "ollama")
	}
	if provider[0].Summary.TotalCostUSD != 3.0 {
		t.Fatalf("ollama cost = %f, want 3.0", provider[0].Summary.TotalCostUSD)
	}

	grouped, err := s.SummaryByGroup("resource", start, end)
	if err != nil {
		t.Fatalf("SummaryByGroup(resource): %v", err)
	}
	if len(grouped) != len(resource) {
		t.Fatalf("grouped resource len = %d, want %d", len(grouped), len(resource))
	}

	deployment, err := s.SummaryByGroup("deployment", start, end)
	if err != nil {
		t.Fatalf("SummaryByGroup(deployment): %v", err)
	}
	model, err := s.SummaryByGroup("model", start, end)
	if err != nil {
		t.Fatalf("SummaryByGroup(model): %v", err)
	}
	if len(deployment) != len(model) {
		t.Fatalf("deployment groups len = %d, want %d", len(deployment), len(model))
	}
	if deployment[0].Key != model[0].Key {
		t.Fatalf("deployment first key = %q, want %q", deployment[0].Key, model[0].Key)
	}
}

func TestSummaryByGroup_InvalidGroup(t *testing.T) {
	s := testStore(t)

	start := time.Now().Add(-1 * time.Hour)
	end := time.Now()
	_, err := s.SummaryByGroup("bogus", start, end)
	if err == nil {
		t.Fatal("expected invalid group_by error")
	}
}

func TestComputeCost(t *testing.T) {
	pricing := testPricing()

	tests := []struct {
		name   string
		model  string
		input  int
		output int
		want   float64
	}{
		{"opus_normal", "claude-opus-4-20250514", 1_000_000, 100_000, 22.5},              // 15 + 7.5
		{"qualified_opus", "anthropic/claude-opus-4-20250514", 1_000_000, 100_000, 22.5}, // upstream fallback
		{"sonnet_normal", "claude-sonnet-4-20250514", 1_000_000, 100_000, 4.5},           // 3 + 1.5
		{"unknown_model", "gpt-oss:120b", 1_000_000, 1_000_000, 0},                       // not in pricing
		{"zero_tokens", "claude-opus-4-20250514", 0, 0, 0},
		{"small_usage", "claude-opus-4-20250514", 1000, 500, 0.0525}, // 0.015 + 0.0375
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeCost(tt.model, tt.input, tt.output, pricing)
			if diff := got - tt.want; diff > 0.0001 || diff < -0.0001 {
				t.Errorf("ComputeCost(%q, %d, %d) = %f, want %f", tt.model, tt.input, tt.output, got, tt.want)
			}
		})
	}
}

func TestComputeDetailedCostForIdentity_AnthropicCacheBuckets(t *testing.T) {
	pricing := testPricing()
	identity := ModelIdentity{
		Model:         "anthropic/claude-opus-4-20250514",
		UpstreamModel: "claude-opus-4-20250514",
		Resource:      "anthropic",
		Provider:      "anthropic",
	}

	// Legacy call without TTL breakdown: all cache-writes billed at 5m
	// rate (1.25×), matching pre-#736 behavior for historical records.
	got := ComputeDetailedCostForIdentity(identity, 1_000_000, 1_000_000, 1_000_000, 100_000, pricing)
	want := 15.0 + (15.0 * 1.25) + (15.0 * 0.10) + 7.5
	if diff := got - want; diff > 0.0001 || diff < -0.0001 {
		t.Fatalf("ComputeDetailedCostForIdentity(...) = %f, want %f", got, want)
	}
}

func TestComputeDetailedCostForIdentityWithTTL_ChargesFiveMinuteAndOneHour(t *testing.T) {
	pricing := testPricing()
	identity := ModelIdentity{
		Model:         "claude-opus-4-20250514",
		UpstreamModel: "claude-opus-4-20250514",
		Provider:      "anthropic",
	}

	// 1M uncached input + 1M 5m writes + 1M 1h writes + 1M reads + 100k output.
	// Opus pricing: input $15/MTok, output $75/MTok.
	// 5m write multiplier: 1.25×. 1h: 2.0×. Read: 0.1×.
	got := ComputeDetailedCostForIdentityWithTTL(identity,
		1_000_000, // uncached input
		2_000_000, // total cache writes (5m + 1h attributed)
		1_000_000, // 5m bucket
		1_000_000, // 1h bucket
		1_000_000, // cache reads
		100_000,   // output
		pricing,
	)
	want := 15.0 /* uncached input */ +
		(15.0 * 1.25) /* 5m write */ +
		(15.0 * 2.00) /* 1h write */ +
		(15.0 * 0.10) /* cache read */ +
		7.5 /* output */
	if diff := got - want; diff > 0.0001 || diff < -0.0001 {
		t.Fatalf("cost = %f, want %f", got, want)
	}
}

func TestComputeDetailedCostForIdentityWithTTL_UnattributedFallsBackTo5m(t *testing.T) {
	pricing := testPricing()
	identity := ModelIdentity{
		Model:         "claude-opus-4-20250514",
		UpstreamModel: "claude-opus-4-20250514",
		Provider:      "anthropic",
	}

	// Provider reported 1M total writes but attributed 0 to each
	// bucket. Matches the pre-#736 schema where the breakdown columns
	// didn't exist. Must fall back to 5m multiplier on the full total.
	got := ComputeDetailedCostForIdentityWithTTL(identity,
		0,         // no uncached input
		1_000_000, // total writes
		0, 0,      // no attribution
		0, 0, // no reads, no output
		pricing,
	)
	want := 15.0 * 1.25
	if diff := got - want; diff > 0.0001 || diff < -0.0001 {
		t.Fatalf("unattributed cost = %f, want %f (5m rate)", got, want)
	}
}

func TestComputeCost_NilPricing(t *testing.T) {
	got := ComputeCost("claude-opus-4-20250514", 1000, 500, nil)
	if got != 0 {
		t.Errorf("ComputeCost with nil pricing = %f, want 0", got)
	}
}

func TestRecord_AutoID(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	rec := Record{
		Timestamp: time.Now(),
		RequestID: "r_test",
		Model:     "m",
		Provider:  "p",
		Role:      "interactive",
	}
	if err := s.Record(ctx, rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Verify the record was stored (summary should show 1 record).
	start := time.Now().Add(-1 * time.Minute)
	end := time.Now().Add(1 * time.Minute)
	sum, err := s.Summary(start, end)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum.TotalRecords != 1 {
		t.Errorf("TotalRecords = %d, want 1", sum.TotalRecords)
	}
}

func TestNewStore_NilDB(t *testing.T) {
	_, err := NewStore(nil)
	if err == nil {
		t.Error("NewStore(nil) should fail")
	}
}

func TestResolveProvider(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"claude-opus-4-20250514", "anthropic"},
		{"anthropic/claude-opus-4-20250514", "anthropic"},
		{"claude-sonnet-4-20250514", "anthropic"},
		{"claude-haiku-3-20240307", "anthropic"},
		{"llama3.2:latest", "ollama"},
		{"qwen2.5:7b", "ollama"},
		{"", "ollama"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := ResolveProvider(tt.model)
			if got != tt.want {
				t.Errorf("ResolveProvider(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestResolveModelIdentity_WithCatalog(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Models.Resources = map[string]config.ModelServerConfig{
		"edge": {URL: "http://edge.example:11434", Provider: "ollama"},
	}
	cfg.Models.Available = []config.ModelConfig{
		{Name: "qwen3:8b", Resource: "edge", SupportsTools: true, ContextWindow: 32768, Speed: 7, Quality: 6, CostTier: 0},
	}

	cat, err := fleet.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("BuildCatalog: %v", err)
	}

	identity := ResolveModelIdentity("edge/qwen3:8b", cat)
	if identity.Model != "edge/qwen3:8b" {
		t.Fatalf("Model = %q, want %q", identity.Model, "edge/qwen3:8b")
	}
	if identity.UpstreamModel != "qwen3:8b" {
		t.Fatalf("UpstreamModel = %q, want %q", identity.UpstreamModel, "qwen3:8b")
	}
	if identity.Resource != "edge" {
		t.Fatalf("Resource = %q, want %q", identity.Resource, "edge")
	}
	if identity.Provider != "ollama" {
		t.Fatalf("Provider = %q, want %q", identity.Provider, "ollama")
	}
}

func TestRecord_PersistsDeploymentMetadata(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	rec := Record{
		Timestamp:                time.Now().UTC(),
		RequestID:                "r_meta",
		SessionID:                "sess-meta",
		ConversationID:           "conv-meta",
		Model:                    "mirror/claude-opus-4-20250514",
		UpstreamModel:            "claude-opus-4-20250514",
		Resource:                 "mirror",
		Provider:                 "anthropic",
		InputTokens:              100,
		OutputTokens:             50,
		CacheCreationInputTokens: 64,
		CacheReadInputTokens:     128,
		CostUSD:                  1.23,
		Role:                     "interactive",
	}
	if err := s.Record(ctx, rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var got Record
	row := s.db.QueryRowContext(ctx, `
		SELECT model, upstream_model, resource, provider, input_tokens, output_tokens, cache_creation_input_tokens, cache_read_input_tokens, cost_usd
		FROM usage_records
		WHERE request_id = ?`,
		rec.RequestID,
	)
	if err := row.Scan(&got.Model, &got.UpstreamModel, &got.Resource, &got.Provider, &got.InputTokens, &got.OutputTokens, &got.CacheCreationInputTokens, &got.CacheReadInputTokens, &got.CostUSD); err != nil {
		t.Fatalf("scan usage record: %v", err)
	}
	if got.Model != rec.Model {
		t.Fatalf("Model = %q, want %q", got.Model, rec.Model)
	}
	if got.UpstreamModel != rec.UpstreamModel {
		t.Fatalf("UpstreamModel = %q, want %q", got.UpstreamModel, rec.UpstreamModel)
	}
	if got.Resource != rec.Resource {
		t.Fatalf("Resource = %q, want %q", got.Resource, rec.Resource)
	}
	if got.Provider != rec.Provider {
		t.Fatalf("Provider = %q, want %q", got.Provider, rec.Provider)
	}
	if got.CacheCreationInputTokens != rec.CacheCreationInputTokens {
		t.Fatalf("CacheCreationInputTokens = %d, want %d", got.CacheCreationInputTokens, rec.CacheCreationInputTokens)
	}
	if got.CacheReadInputTokens != rec.CacheReadInputTokens {
		t.Fatalf("CacheReadInputTokens = %d, want %d", got.CacheReadInputTokens, rec.CacheReadInputTokens)
	}
}

func TestMigrate_AddsDeploymentMetadataColumns(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE usage_records (
			id TEXT PRIMARY KEY,
			timestamp TEXT NOT NULL,
			request_id TEXT NOT NULL,
			session_id TEXT,
			conversation_id TEXT,
			model TEXT NOT NULL,
			provider TEXT NOT NULL,
			input_tokens INTEGER NOT NULL,
			output_tokens INTEGER NOT NULL,
			cost_usd REAL NOT NULL,
			role TEXT NOT NULL,
			task_name TEXT
		);
	`)
	if err != nil {
		t.Fatalf("create legacy table: %v", err)
	}

	s, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if s == nil {
		t.Fatal("NewStore returned nil store")
	}
	if !database.HasColumn(db, "usage_records", "upstream_model") {
		t.Fatal("expected upstream_model column after migration")
	}
	if !database.HasColumn(db, "usage_records", "resource") {
		t.Fatal("expected resource column after migration")
	}
	if !database.HasColumn(db, "usage_records", "cache_creation_input_tokens") {
		t.Fatal("expected cache_creation_input_tokens column after migration")
	}
	if !database.HasColumn(db, "usage_records", "cache_read_input_tokens") {
		t.Fatal("expected cache_read_input_tokens column after migration")
	}
}
