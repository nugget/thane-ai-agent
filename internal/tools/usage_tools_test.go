package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
)

func testUsageStore(t *testing.T) *usage.Store {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := usage.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func costSummaryFixture(t *testing.T, records ...usage.Record) (*usage.Store, *Tool) {
	t.Helper()
	store := testUsageStore(t)
	for _, rec := range records {
		if rec.Timestamp.IsZero() {
			rec.Timestamp = time.Now().Add(-time.Minute)
		}
		if err := store.Record(t.Context(), rec); err != nil {
			t.Fatal(err)
		}
	}
	reg := NewRegistry(nil, nil, nil)
	reg.SetUsageStore(store)
	tool := reg.Get("cost_summary")
	if tool == nil {
		t.Fatal("cost_summary was not registered")
	}
	return store, tool
}

func invokeCostSummary(t *testing.T, ctx context.Context, tool *Tool, args map[string]any) (costSummaryResult, string) {
	t.Helper()
	raw, err := tool.Handler(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	var result costSummaryResult
	if !utf8.ValidString(raw) || !json.Valid([]byte(raw)) {
		t.Fatalf("tool did not return valid UTF-8 JSON (%d bytes)", len(raw))
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	return result, raw
}

func TestCostSummaryToolPricingCoverage(t *testing.T) {
	_, tool := costSummaryFixture(t,
		usage.Record{Model: "paid", PricingStatus: "priced", CostUSD: 1, InputTokens: 100, OutputTokens: 10, CacheCreationInputTokens: 30, CacheReadInputTokens: 40},
		usage.Record{Model: "local", PricingStatus: "priced", InputTokens: 200},
		usage.Record{Model: "missing-price", PricingStatus: "unpriced", InputTokens: 300},
		usage.Record{Model: "historical", CostUSD: .5, InputTokens: 400},
		usage.Record{Model: "future-status", PricingStatus: "unrecognized", CostUSD: .25, InputTokens: 500},
	)
	got, raw := invokeCostSummary(t, t.Context(), tool, map[string]any{"period": "all"})
	want := usage.Summary{TotalRecords: 5, TotalInputTokens: 1500, TotalOutputTokens: 10,
		TotalCacheCreationInputTokens: 30, TotalCacheReadInputTokens: 40,
		TotalCostUSD: 1.75, PricedRecords: 2, UnpricedRecords: 1, UnknownPricingRecords: 2}
	if got.Summary != want || got.Scope.Kind != "all_recorded_usage" || got.GroupBy != "" || len(got.Groups) != 0 || got.Truncated {
		t.Fatalf("summary=%+v scope=%+v grouping=%q groups=%d truncated=%v", got.Summary, got.Scope, got.GroupBy, len(got.Groups), got.Truncated)
	}
	notes := strings.ToLower(strings.Join(got.Notes, " "))
	for _, phrase := range []string{"missing prices", "pricing coverage is unknown"} {
		if !strings.Contains(notes, phrase) {
			t.Errorf("notes omitted %q: %v", phrase, got.Notes)
		}
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"scope", "window", "summary", "unattributed", "matched_groups", "returned_groups", "truncated"} {
		if _, ok := envelope[key]; !ok {
			t.Errorf("missing contract field %q", key)
		}
	}
	if got.Window.Period != "all" || got.Window.Since != "" || got.Window.Until == "" || got.Window.Bounds == "" {
		t.Errorf("all-time window=%+v", got.Window)
	}
}

func TestCostSummaryToolPricingWarningsAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		name, status     string
		missing, unknown bool
	}{
		{"configured free", "priced", false, false},
		{"missing", "unpriced", true, false},
		{"legacy", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, tool := costSummaryFixture(t, usage.Record{PricingStatus: tc.status, InputTokens: 10})
			got, _ := invokeCostSummary(t, t.Context(), tool, map[string]any{"period": "all"})
			notes := strings.ToLower(strings.Join(got.Notes, " "))
			if strings.Contains(notes, "missing prices") != tc.missing || strings.Contains(notes, "pricing coverage is unknown") != tc.unknown {
				t.Errorf("wrong pricing guidance: %v", got.Notes)
			}
		})
	}
}

func TestCostSummaryToolSelfScope(t *testing.T) {
	_, tool := costSummaryFixture(t,
		usage.Record{LoopID: "self-id", LoopName: "worker", Purpose: "agent", CostUSD: 1, PricingStatus: "priced"},
		usage.Record{LoopID: "self-id", LoopName: "worker", Purpose: "compaction", CostUSD: .25, PricingStatus: "priced"},
		usage.Record{LoopID: "child-id", ParentLoopID: "self-id", LoopName: "delegate", CostUSD: 9},
		usage.Record{LoopID: "other-id", LoopName: "worker", CostUSD: 10},
		usage.Record{CostUSD: 5, InputTokens: 500},
	)
	got, _ := invokeCostSummary(t, WithLoopID(t.Context(), "self-id"), tool, map[string]any{"period": "all", "loop_id": "self"})
	if got.Scope.Kind != "loop_id" || got.Scope.LoopID != "self-id" || got.Summary.TotalRecords != 2 || got.Summary.TotalCostUSD != 1.25 {
		t.Fatalf("self scope included unrelated work: scope=%+v summary=%+v", got.Scope, got.Summary)
	}
	if got.GroupBy != "loop" || got.MatchedGroups != 1 || got.ReturnedGroups != 1 || len(got.Groups) != 1 || got.Groups[0].Key != "self-id" || got.Groups[0].LoopName != "worker" {
		t.Errorf("self grouping=%+v", got)
	}
	if got.Unattributed != (usage.Summary{}) {
		t.Errorf("self selection included unattributed work: %+v", got.Unattributed)
	}
	global, _ := invokeCostSummary(t, t.Context(), tool, map[string]any{"period": "all", "group_by": "loop"})
	if global.Unattributed.TotalRecords != 1 || global.Unattributed.TotalCostUSD != 5 || global.MatchedGroups != 3 {
		t.Errorf("global coverage lost unattributed work or invented a loop: %+v", global)
	}
}

func TestCostSummaryToolHistoricalNameKeepsDistinctIDs(t *testing.T) {
	_, tool := costSummaryFixture(t,
		usage.Record{LoopID: "retired", LoopName: "archivist", CostUSD: 3},
		usage.Record{LoopID: "replacement", LoopName: "archivist", CostUSD: 4},
		usage.Record{LoopID: "unrelated", LoopName: "other", CostUSD: 100},
	)
	got, _ := invokeCostSummary(t, t.Context(), tool, map[string]any{"period": "all", "loop_name": "archivist"})
	if got.Scope.Kind != "recorded_loop_name" || got.Scope.LoopName != "archivist" || got.Summary.TotalCostUSD != 7 || got.GroupBy != "loop" || got.MatchedGroups != 2 || len(got.Groups) != 2 {
		t.Fatalf("historical name selection=%+v", got)
	}
	if got.Groups[0].Key != "replacement" || got.Groups[1].Key != "retired" {
		t.Errorf("distinct loop IDs or cost ordering lost: %+v", got.Groups)
	}
}

func TestCostSummaryToolGroupModes(t *testing.T) {
	_, tool := costSummaryFixture(t, usage.Record{Model: "deployment-id", UpstreamModel: "wire-model", Provider: "provider-family",
		Resource: "host", Role: "autonomous", TaskName: "poll", LoopID: "loop-id", LoopName: "name", CostUSD: 1})
	for group, key := range map[string]string{
		"deployment": "deployment-id", "model": "deployment-id", "upstream_model": "wire-model",
		"provider": "provider-family", "resource": "host", "role": "autonomous", "task": "poll", "loop": "loop-id",
	} {
		t.Run(group, func(t *testing.T) {
			got, _ := invokeCostSummary(t, t.Context(), tool, map[string]any{"period": "all", "group_by": group})
			if len(got.Groups) != 1 || got.Groups[0].Key != key || got.Groups[0].Summary.TotalCostUSD != 1 || got.MatchedGroups != 1 || got.ReturnedGroups != 1 {
				t.Fatalf("group %q=%+v", group, got)
			}
		})
	}
	got, _ := invokeCostSummary(t, t.Context(), tool, map[string]any{"period": "all", "group_by": " Provider "})
	if got.GroupBy != "provider" || len(got.Groups) != 1 || got.Groups[0].Key != "provider-family" {
		t.Errorf("normalized grouping=%+v", got)
	}
}

func TestCostSummaryToolFiltersBeforeGroupLimit(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	since, until := now.Add(-2*time.Hour), now.Add(-time.Hour)
	_, tool := costSummaryFixture(t,
		usage.Record{Timestamp: since, LoopID: "selected", Model: "inside-high", CostUSD: 2},
		usage.Record{Timestamp: since.Add(time.Minute), LoopID: "selected", Model: "inside-low", CostUSD: 1},
		usage.Record{Timestamp: until, LoopID: "selected", Model: "outside-window", CostUSD: 100},
		usage.Record{Timestamp: since, LoopID: "unrelated", Model: "outside-loop", CostUSD: 200},
	)
	got, _ := invokeCostSummary(t, t.Context(), tool, map[string]any{
		"since": since.Format(time.RFC3339), "until": until.Format(time.RFC3339),
		"loop_id": "selected", "group_by": "model", "limit": float64(1),
	})
	if got.Summary.TotalRecords != 2 || got.Summary.TotalCostUSD != 3 || got.MatchedGroups != 2 || got.ReturnedGroups != 1 || !got.Truncated || len(got.Groups) != 1 || got.Groups[0].Key != "inside-high" {
		t.Fatalf("filter/limit ordering lost matching rows or complete total: %+v", got)
	}
	if got.Window.Period != "" || !strings.HasPrefix(got.Window.Since, "-") || !strings.HasPrefix(got.Window.Until, "-") {
		t.Errorf("custom window must expose relative bounds: %+v", got.Window)
	}
}

func TestCostSummaryToolEmptyScopeAndMissingSelfDiffer(t *testing.T) {
	_, tool := costSummaryFixture(t)
	got, _ := invokeCostSummary(t, t.Context(), tool, map[string]any{"loop_id": "not-recorded", "period": "all"})
	if got.Summary.TotalRecords != 0 || got.MatchedGroups != 0 || got.ReturnedGroups != 0 || len(got.Groups) != 0 || got.Truncated {
		t.Fatalf("empty selection=%+v", got)
	}
	notes := strings.ToLower(strings.Join(got.Notes, " "))
	if !strings.Contains(notes, "no ") || !strings.Contains(notes, "record") {
		t.Errorf("empty result omitted guidance: %v", got.Notes)
	}
	if _, err := tool.Handler(t.Context(), map[string]any{"loop_id": "self"}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "loop") {
		t.Fatalf("missing self context error=%v", err)
	}
}

func TestCostSummaryToolBoundedJSON(t *testing.T) {
	for _, tc := range []struct {
		name           string
		count, repeats int
	}{
		{"many Unicode labels", 30, 200}, {"oversized first group", 1, 5000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var records []usage.Record
			for i := range tc.count {
				records = append(records, usage.Record{LoopID: fmt.Sprintf("id-%03d", i), LoopName: strings.Repeat("界🌌", tc.repeats), CostUSD: 1})
			}
			_, tool := costSummaryFixture(t, records...)
			got, raw := invokeCostSummary(t, t.Context(), tool, map[string]any{"period": "all", "group_by": "loop", "limit": 100})
			if len(raw) > 16*1024 || !got.Truncated || got.MatchedGroups != tc.count || got.ReturnedGroups != len(got.Groups) || len(got.Groups) >= tc.count {
				t.Fatalf("bounded result bytes=%d matched=%d returned=%d groups=%d truncated=%v", len(raw), got.MatchedGroups, got.ReturnedGroups, len(got.Groups), got.Truncated)
			}
			if got.Summary.TotalRecords != tc.count || got.Summary.TotalCostUSD != float64(tc.count) {
				t.Errorf("response cap altered total: %+v", got.Summary)
			}
		})
	}
}

func TestCostSummaryToolRejectsInvalidArguments(t *testing.T) {
	_, tool := costSummaryFixture(t)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"unknown period", map[string]any{"period": "ever"}},
		{"unknown group", map[string]any{"group_by": "bogus"}},
		{"selector conflict", map[string]any{"loop_id": "id", "loop_name": "name"}},
		{"period with since", map[string]any{"period": "all", "since": "-1h"}},
		{"period with until", map[string]any{"period": "today", "until": "-1h"}},
		{"until without since", map[string]any{"until": "-1h"}},
		{"reversed window", map[string]any{"since": "-1h", "until": "-2h"}},
		{"empty window", map[string]any{"since": "-1h", "until": "-1h"}},
		{"malformed time", map[string]any{"since": "yesterday evening"}},
		{"unsigned duration", map[string]any{"since": "1h"}},
		{"limit zero", map[string]any{"limit": 0}},
		{"limit negative", map[string]any{"limit": -1}},
		{"limit too high", map[string]any{"limit": 101}},
		{"limit fractional", map[string]any{"limit": 1.5}},
		{"limit boolean", map[string]any{"limit": true}},
		{"limit NaN", map[string]any{"limit": math.NaN()}},
		{"limit infinite", map[string]any{"limit": math.Inf(1)}},
		{"oversized name", map[string]any{"loop_name": strings.Repeat("界", 257)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tool.Handler(t.Context(), tc.args); err == nil {
				t.Fatal("invalid arguments were accepted")
			}
		})
	}
	for _, key := range []string{"period", "since", "until", "group_by", "loop_id", "loop_name"} {
		for _, value := range []any{"", "   ", true, 3, nil} {
			t.Run(fmt.Sprintf("%s/%v", key, value), func(t *testing.T) {
				if _, err := tool.Handler(t.Context(), map[string]any{key: value}); err == nil {
					t.Fatal("empty or wrong-typed argument was accepted")
				}
			})
		}
	}
}

func TestCostSummaryToolCancellation(t *testing.T) {
	_, tool := costSummaryFixture(t, usage.Record{InputTokens: 1})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := tool.Handler(ctx, map[string]any{"period": "all", "group_by": "model"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation became a successful empty report: %v", err)
	}
}

func TestParseCostSummaryWindows(t *testing.T) {
	zone := time.FixedZone("household", -6*60*60)
	now := time.Date(2026, time.September, 16, 14, 15, 16, 0, zone)
	today := time.Date(2026, time.September, 16, 0, 0, 0, 0, zone)
	for _, tc := range []struct {
		name       string
		args       map[string]any
		period     string
		start, end time.Time
	}{
		{"default", nil, "today", today, now},
		{"today", map[string]any{"period": "today"}, "today", today, now},
		{"yesterday", map[string]any{"period": "yesterday"}, "yesterday", today.AddDate(0, 0, -1), today},
		{"week", map[string]any{"period": "week"}, "week", now.AddDate(0, 0, -7), now},
		{"month", map[string]any{"period": "month"}, "month", now.AddDate(0, -1, 0), now},
		{"all", map[string]any{"period": "all"}, "all", time.Time{}, now},
		{"signed custom", map[string]any{"since": "-2h", "until": "-1h"}, "", now.Add(-2 * time.Hour), now.Add(-time.Hour)},
		{"custom until defaults now", map[string]any{"since": "-1d"}, "", now.Add(-24 * time.Hour), now},
		{"RFC3339", map[string]any{"since": "2026-09-01T00:00:00Z", "until": "2026-09-02T00:00:00Z"}, "", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, period, err := parseCostSummaryOptions(t.Context(), tc.args, now)
			if err != nil {
				t.Fatal(err)
			}
			if !opts.Start.Equal(tc.start) || !opts.End.Equal(tc.end) || period != tc.period || opts.Limit != 20 {
				t.Errorf("window=%v..%v period=%q limit=%d; want %v..%v period=%q limit=20", opts.Start, opts.End, period, opts.Limit, tc.start, tc.end, tc.period)
			}
		})
	}
}

func TestCostSummaryToolLimitDefaultsAndCoercion(t *testing.T) {
	var records []usage.Record
	for i := range 25 {
		records = append(records, usage.Record{Model: fmt.Sprintf("model-%02d", i), CostUSD: 1})
	}
	_, tool := costSummaryFixture(t, records...)
	got, _ := invokeCostSummary(t, t.Context(), tool, map[string]any{"period": "all", "group_by": "model"})
	if got.MatchedGroups != 25 || got.ReturnedGroups != 20 || !got.Truncated || got.Summary.TotalCostUSD != 25 {
		t.Fatalf("default limit lost groups or totals: %+v", got)
	}
	for _, limit := range []any{int(2), int32(2), int64(2), float64(2), json.Number("2"), " 2 "} {
		got, _ := invokeCostSummary(t, t.Context(), tool, map[string]any{"period": "all", "group_by": "model", "limit": limit})
		if got.MatchedGroups != 25 || got.ReturnedGroups != 2 || len(got.Groups) != 2 || !got.Truncated || got.Summary.TotalCostUSD != 25 {
			t.Errorf("limit %T(%v) changed matching totals or returned count: %+v", limit, limit, got)
		}
	}
}

func TestSetUsageStoreNilStore(t *testing.T) {
	reg := NewRegistry(nil, nil, nil)
	reg.SetUsageStore(nil)
	if reg.Get("cost_summary") != nil {
		t.Error("cost_summary registered without a usage store")
	}
}
