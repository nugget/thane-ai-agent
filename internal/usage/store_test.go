package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/config"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "usage_test.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore(%q): %v", dbPath, err)
	}
	t.Cleanup(func() { s.Close() })
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
			Timestamp:      now,
			RequestID:      "r_001",
			SessionID:      "sess-1",
			ConversationID: "conv-1",
			Model:          "claude-opus-4-20250514",
			Provider:       "anthropic",
			InputTokens:    1000,
			OutputTokens:   500,
			CostUSD:        0.0525, // 1000/1M*15 + 500/1M*75
			Role:           "interactive",
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
	// 0.0525 + 0.021 = 0.0735
	if diff := sum.TotalCostUSD - 0.0735; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("TotalCostUSD = %f, want ~0.0735", sum.TotalCostUSD)
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

func TestComputeCost(t *testing.T) {
	pricing := testPricing()

	tests := []struct {
		name   string
		model  string
		input  int
		output int
		want   float64
	}{
		{"opus_normal", "claude-opus-4-20250514", 1_000_000, 100_000, 22.5},    // 15 + 7.5
		{"sonnet_normal", "claude-sonnet-4-20250514", 1_000_000, 100_000, 4.5}, // 3 + 1.5
		{"unknown_model", "gpt-oss:120b", 1_000_000, 1_000_000, 0},             // not in pricing
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

func TestNewStore_InvalidPath(t *testing.T) {
	_, err := NewStore("/nonexistent/path/usage.db")
	if err == nil {
		t.Error("NewStore() should fail for invalid path")
	}
}

func TestResolveProvider(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"claude-opus-4-20250514", "anthropic"},
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
