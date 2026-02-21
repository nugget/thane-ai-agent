package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/usage"
)

func testUsageStore(t *testing.T) *usage.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "usage_test.db")
	s, err := usage.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore(%q): %v", dbPath, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		name string
		n    int64
		want string
	}{
		{"millions", 1_230_000, "1.23M"},
		{"exact_million", 1_000_000, "1.00M"},
		{"thousands", 456_000, "456.0K"},
		{"exact_thousand", 1_000, "1.0K"},
		{"small", 789, "789"},
		{"zero", 0, "0"},
		{"large", 12_345_678, "12.35M"},
		{"boundary_below_million", 999_999, "1000.0K"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTokenCount(tt.n)
			if got != tt.want {
				t.Errorf("formatTokenCount(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}

func TestParsePeriod(t *testing.T) {
	tests := []struct {
		name       string
		period     string
		wantRecent bool // true if start should be recent (within last 48h)
	}{
		{"today", "today", true},
		{"week", "week", true},
		{"month", "month", true},
		{"all", "all", false},
		{"unknown_defaults_to_all", "bogus", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := parsePeriod(tt.period)

			// End should always be in the future (now + buffer).
			if end.Before(time.Now().Add(-1 * time.Second)) {
				t.Errorf("end %v should be at or after now", end)
			}

			if tt.wantRecent {
				// Start should be within the last ~32 days.
				cutoff := time.Now().AddDate(0, -2, 0)
				if start.Before(cutoff) {
					t.Errorf("start %v too far in the past for period %q", start, tt.period)
				}
			} else {
				// "all" and unknown should use zero time.
				if !start.IsZero() {
					t.Errorf("start should be zero for period %q, got %v", tt.period, start)
				}
			}
		})
	}
}

func TestParsePeriod_TodayBounds(t *testing.T) {
	start, _ := parsePeriod("today")
	now := time.Now()
	expectedStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if !start.Equal(expectedStart) {
		t.Errorf("today start = %v, want %v", start, expectedStart)
	}
}

func TestParsePeriod_YesterdayBounds(t *testing.T) {
	start, end := parsePeriod("yesterday")
	now := time.Now()
	yesterday := now.AddDate(0, 0, -1)
	expectedStart := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, yesterday.Location())
	expectedEnd := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if !start.Equal(expectedStart) {
		t.Errorf("yesterday start = %v, want %v", start, expectedStart)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("yesterday end = %v, want %v", end, expectedEnd)
	}
}

func TestCostSummaryTool_Registration(t *testing.T) {
	store := testUsageStore(t)
	reg := NewRegistry(nil, nil)
	reg.SetUsageStore(store)

	tool := reg.Get("cost_summary")
	if tool == nil {
		t.Fatal("cost_summary tool not registered")
	}
	if tool.Name != "cost_summary" {
		t.Errorf("tool name = %q, want %q", tool.Name, "cost_summary")
	}
}

func TestCostSummaryTool_EmptyStore(t *testing.T) {
	store := testUsageStore(t)
	reg := NewRegistry(nil, nil)
	reg.SetUsageStore(store)

	tool := reg.Get("cost_summary")
	if tool == nil {
		t.Fatal("cost_summary tool not registered")
	}

	result, err := tool.Handler(context.Background(), map[string]any{
		"period": "all",
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	if !strings.Contains(result, "Total requests: 0") {
		t.Errorf("expected zero requests in output, got:\n%s", result)
	}
	if !strings.Contains(result, "$0.0000") {
		t.Errorf("expected zero cost in output, got:\n%s", result)
	}
}

func TestCostSummaryTool_WithData(t *testing.T) {
	store := testUsageStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	recs := []usage.Record{
		{Timestamp: now, RequestID: "r1", Model: "claude-opus", Provider: "anthropic", InputTokens: 1000, OutputTokens: 500, CostUSD: 1.5, Role: "interactive"},
		{Timestamp: now, RequestID: "r2", Model: "claude-sonnet", Provider: "anthropic", InputTokens: 2000, OutputTokens: 1000, CostUSD: 0.5, Role: "delegate", TaskName: "summarize"},
	}
	for _, rec := range recs {
		if err := store.Record(ctx, rec); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	reg := NewRegistry(nil, nil)
	reg.SetUsageStore(store)

	tool := reg.Get("cost_summary")
	if tool == nil {
		t.Fatal("cost_summary tool not registered")
	}

	result, err := tool.Handler(ctx, map[string]any{
		"period": "all",
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	if !strings.Contains(result, "Total requests: 2") {
		t.Errorf("expected 2 requests, got:\n%s", result)
	}
	if !strings.Contains(result, "$2.0000") {
		t.Errorf("expected $2.0000 total cost, got:\n%s", result)
	}
}

func TestCostSummaryTool_GroupBy(t *testing.T) {
	store := testUsageStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	recs := []usage.Record{
		{Timestamp: now, RequestID: "r1", Model: "opus", Provider: "anthropic", InputTokens: 1000, OutputTokens: 500, CostUSD: 3.0, Role: "interactive"},
		{Timestamp: now, RequestID: "r2", Model: "sonnet", Provider: "anthropic", InputTokens: 2000, OutputTokens: 1000, CostUSD: 1.0, Role: "delegate"},
	}
	for _, rec := range recs {
		if err := store.Record(ctx, rec); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	reg := NewRegistry(nil, nil)
	reg.SetUsageStore(store)

	tool := reg.Get("cost_summary")

	tests := []struct {
		name     string
		groupBy  string
		wantText string
	}{
		{"by_model", "model", "By Model:"},
		{"by_role", "role", "By Role:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Handler(ctx, map[string]any{
				"period":   "all",
				"group_by": tt.groupBy,
			})
			if err != nil {
				t.Fatalf("handler error: %v", err)
			}
			if !strings.Contains(result, tt.wantText) {
				t.Errorf("expected %q in output, got:\n%s", tt.wantText, result)
			}
		})
	}
}

func TestCostSummaryTool_GroupByOrdering(t *testing.T) {
	store := testUsageStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	recs := []usage.Record{
		{Timestamp: now, RequestID: "r1", Model: "cheap", Provider: "ollama", InputTokens: 100, OutputTokens: 50, CostUSD: 0.01, Role: "interactive"},
		{Timestamp: now, RequestID: "r2", Model: "expensive", Provider: "anthropic", InputTokens: 100, OutputTokens: 50, CostUSD: 10.0, Role: "interactive"},
	}
	for _, rec := range recs {
		if err := store.Record(ctx, rec); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	reg := NewRegistry(nil, nil)
	reg.SetUsageStore(store)

	tool := reg.Get("cost_summary")
	result, err := tool.Handler(ctx, map[string]any{
		"period":   "all",
		"group_by": "model",
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	// "expensive" should appear before "cheap" in output (cost DESC).
	expIdx := strings.Index(result, "expensive")
	cheapIdx := strings.Index(result, "cheap")
	if expIdx == -1 || cheapIdx == -1 {
		t.Fatalf("expected both models in output, got:\n%s", result)
	}
	if expIdx > cheapIdx {
		t.Errorf("expensive should appear before cheap (cost DESC order), got:\n%s", result)
	}
}

func TestSetUsageStore_NilStore(t *testing.T) {
	reg := NewRegistry(nil, nil)
	reg.SetUsageStore(nil)

	tool := reg.Get("cost_summary")
	if tool != nil {
		t.Error("cost_summary should not be registered with nil store")
	}
}
