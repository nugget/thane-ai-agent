package usage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func reportFixture(t *testing.T) (*Store, ReportOptions) {
	t.Helper()
	s := testStore(t)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	records := []Record{
		{ID: "a1", LoopID: "a", LoopName: "review", CostUSD: 1, PricingStatus: "priced"},
		{ID: "a2", LoopID: "a", LoopName: "renamed", CostUSD: 2, PricingStatus: "priced", Model: "different-model"},
		{ID: "b", LoopID: "b", LoopName: "review", ParentLoopID: "a", CostUSD: 3, PricingStatus: "priced"},
		{ID: "c", LoopID: "c", LoopName: "other", CostUSD: 1, PricingStatus: "unpriced"},
		{ID: "legacy", CostUSD: 7},
		{ID: "named-unattributed", LoopName: "review", CostUSD: 1, PricingStatus: "unpriced"},
	}
	for i, rec := range records {
		rec.Timestamp = now.Add(time.Duration(i) * time.Second)
		rec.InputTokens, rec.OutputTokens = 10, 2
		rec.CacheCreationInputTokens, rec.CacheReadInputTokens = 3, 4
		if rec.LoopID != "" {
			if rec.Model == "" {
				rec.Model = "model"
			}
			rec.UpstreamModel, rec.Provider, rec.Resource = "upstream", "provider", "resource"
			rec.Role, rec.TaskName = "autonomous", "task"
		}
		if err := s.Record(t.Context(), rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Record(t.Context(), Record{ID: "outside", Timestamp: now.Add(time.Hour), LoopID: "a", LoopName: "outside", CostUSD: 100}); err != nil {
		t.Fatal(err)
	}
	return s, ReportOptions{Start: now, End: now.Add(time.Minute), GroupBy: "loop", Limit: 1}
}

func TestReportLoopSelectionAndCoverage(t *testing.T) {
	t.Parallel()
	s, defaults := reportFixture(t)
	for _, tt := range []struct {
		name           string
		loopID         string
		loopName       string
		wantRecords    int
		wantCost       float64
		wantUnknown    int
		wantUnknownUSD float64
		wantGroups     int
		wantKey        string
		wantLabel      string
		wantGroupCost  float64
		wantTruncated  bool
	}{
		{name: "global totals survive group cap", wantRecords: 6, wantCost: 15, wantUnknown: 2, wantUnknownUSD: 8,
			wantGroups: 3, wantKey: "a", wantLabel: "renamed", wantGroupCost: 3, wantTruncated: true},
		{name: "exact id includes old names but not child", loopID: "a", wantRecords: 2, wantCost: 3,
			wantGroups: 1, wantKey: "a", wantLabel: "renamed", wantGroupCost: 3},
		{name: "captured name preserves different ids and unknown ids", loopName: "review", wantRecords: 3, wantCost: 5,
			wantUnknown: 1, wantUnknownUSD: 1, wantGroups: 2, wantKey: "b", wantLabel: "review", wantGroupCost: 3, wantTruncated: true},
		{name: "absent historical id is empty", loopID: "not-in-live-registry"},
		{name: "name is exact not a pattern", loopName: "rev%"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := defaults
			opts.LoopID, opts.LoopName = tt.loopID, tt.loopName
			report, err := s.Report(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if report.Summary.TotalRecords != tt.wantRecords || report.Summary.TotalCostUSD != tt.wantCost || report.Summary.TotalInputTokens != int64(tt.wantRecords*10) || report.Summary.TotalOutputTokens != int64(tt.wantRecords*2) || report.Summary.TotalCacheCreationInputTokens != int64(tt.wantRecords*3) || report.Summary.TotalCacheReadInputTokens != int64(tt.wantRecords*4) {
				t.Errorf("totals = %+v", report.Summary)
			}
			if report.Summary.PricedRecords+report.Summary.UnpricedRecords+report.Summary.UnknownPricingRecords != tt.wantRecords {
				t.Errorf("pricing coverage does not account for all records: %+v", report.Summary)
			}
			if report.Unattributed.TotalRecords != tt.wantUnknown || report.Unattributed.TotalCostUSD != tt.wantUnknownUSD {
				t.Errorf("unattributed = %+v", report.Unattributed)
			}
			if report.MatchedGroups != tt.wantGroups || report.Truncated != tt.wantTruncated || len(report.Groups) != min(tt.wantGroups, 1) {
				t.Errorf("group counts/truncation = %+v", report)
			}
			if tt.wantGroups > 0 {
				group := report.Groups[0]
				if group.Key != tt.wantKey || group.LoopName != tt.wantLabel || group.Summary.TotalCostUSD != tt.wantGroupCost {
					t.Errorf("group = %+v", group)
				}
			}
			if report.Groups == nil {
				t.Error("groups must serialize as an empty list, not null")
			}
		})
	}
}

func TestReportAllGroupingModesShareTotalsAndFilters(t *testing.T) {
	t.Parallel()
	s, opts := reportFixture(t)
	opts.Limit = 100
	for _, groupBy := range []string{"", "deployment", "model", "upstream_model", "provider", "resource", "role", "task", "loop", "loop_name"} {
		t.Run(groupBy, func(t *testing.T) {
			opts := opts
			opts.GroupBy = groupBy
			report, err := s.Report(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			want := Summary{TotalRecords: 6, TotalInputTokens: 60, TotalOutputTokens: 12,
				TotalCacheCreationInputTokens: 18, TotalCacheReadInputTokens: 24, TotalCostUSD: 15,
				PricedRecords: 3, UnpricedRecords: 2, UnknownPricingRecords: 1}
			if report.Summary != want || report.Truncated {
				t.Fatalf("totals = %+v, truncated=%v; want %+v", report.Summary, report.Truncated, want)
			}
			if groupBy == "" {
				if report.MatchedGroups != 0 || len(report.Groups) != 0 {
					t.Fatalf("total-only report has groups: %+v", report)
				}
				return
			}
			var sum Summary
			for _, group := range report.Groups {
				sum.TotalRecords += group.Summary.TotalRecords
				sum.TotalCostUSD += group.Summary.TotalCostUSD
				sum.PricedRecords += group.Summary.PricedRecords
				sum.UnpricedRecords += group.Summary.UnpricedRecords
				sum.UnknownPricingRecords += group.Summary.UnknownPricingRecords
			}
			if groupBy == "loop" {
				if sum.TotalRecords != 4 || sum.TotalCostUSD != 7 || report.MatchedGroups != 3 {
					t.Fatalf("known-loop group totals = %+v; groups=%d", sum, report.MatchedGroups)
				}
			} else if groupBy == "loop_name" {
				if sum.TotalRecords != 5 || sum.TotalCostUSD != 8 || report.MatchedGroups != 3 {
					t.Fatalf("named-loop group totals = %+v; groups=%d", sum, report.MatchedGroups)
				}
			} else if sum.TotalRecords != want.TotalRecords || sum.TotalCostUSD != want.TotalCostUSD || sum.PricedRecords != want.PricedRecords || sum.UnpricedRecords != want.UnpricedRecords || sum.UnknownPricingRecords != want.UnknownPricingRecords {
				t.Fatalf("group totals = %+v, want complete coverage of %+v", sum, want)
			}
			if len(report.Groups) != report.MatchedGroups {
				t.Error("uncapped list differs from matched count")
			}
			for i := 1; i < len(report.Groups); i++ {
				prev, next := report.Groups[i-1], report.Groups[i]
				if prev.Summary.TotalCostUSD < next.Summary.TotalCostUSD || (prev.Summary.TotalCostUSD == next.Summary.TotalCostUSD && prev.Key > next.Key) {
					t.Fatalf("unstable cost/key ordering: %+v", report.Groups)
				}
			}
			opts.LoopID = "a"
			filtered, err := s.Report(t.Context(), opts)
			if err != nil || filtered.Summary.TotalRecords != 2 || filtered.Summary.TotalCostUSD != 3 || filtered.Unattributed.TotalRecords != 0 {
				t.Fatalf("filtered report = %+v, %v", filtered, err)
			}
		})
	}
}

func TestReportLoopNameCombinesCapturedNamesAcrossInstances(t *testing.T) {
	t.Parallel()
	s, defaults := reportFixture(t)
	if err := s.Record(t.Context(), Record{ID: "unnamed-instance", Timestamp: defaults.Start.Add(10 * time.Second), LoopID: "unnamed", CostUSD: 4}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name         string
		loopID       string
		loopName     string
		limit        int
		wantRecords  int
		wantCost     float64
		unattributed int
		matched      int
		keys         []string
		costs        []float64
		counts       []int
	}{
		{name: "restarted or reused name combines even without ID", limit: 10, wantRecords: 7, wantCost: 19, unattributed: 2, matched: 3,
			keys: []string{"review", "renamed", "other"}, costs: []float64{5, 2, 1}, counts: []int{3, 1, 1}},
		{name: "limited named rows preserve all totals including unnamed", limit: 1, wantRecords: 7, wantCost: 19, unattributed: 2, matched: 3,
			keys: []string{"review"}, costs: []float64{5}, counts: []int{3}},
		{name: "exact name selects all captured matches", loopName: "review", limit: 10, wantRecords: 3, wantCost: 5, unattributed: 1, matched: 1,
			keys: []string{"review"}, costs: []float64{5}, counts: []int{3}},
		{name: "renamed instance retains separate captured names", loopID: "a", limit: 10, wantRecords: 2, wantCost: 3, matched: 2,
			keys: []string{"renamed", "review"}, costs: []float64{2, 1}, counts: []int{1, 1}},
		{name: "unnamed instance remains in totals only", loopID: "unnamed", limit: 10, wantRecords: 1, wantCost: 4},
		{name: "name is an exact label", loopName: "Review", limit: 10},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := defaults
			opts.GroupBy, opts.Limit = "loop_name", tt.limit
			opts.LoopID, opts.LoopName = tt.loopID, tt.loopName
			report, err := s.Report(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if report.Summary.TotalRecords != tt.wantRecords || report.Summary.TotalCostUSD != tt.wantCost || report.Unattributed.TotalRecords != tt.unattributed {
				t.Errorf("complete totals = %+v, unattributed = %+v", report.Summary, report.Unattributed)
			}
			if report.MatchedGroups != tt.matched || len(report.Groups) != len(tt.keys) || report.Truncated != (tt.matched > len(tt.keys)) {
				t.Fatalf("group count/cap = %+v", report)
			}
			for i, key := range tt.keys {
				group := report.Groups[i]
				if group.Key != key || group.LoopName != "" || group.Summary.TotalCostUSD != tt.costs[i] || group.Summary.TotalRecords != tt.counts[i] {
					t.Errorf("group %d = %+v; want key=%q cost=%v records=%d", i, group, key, tt.costs[i], tt.counts[i])
				}
			}
		})
	}
}

func TestReportTimeBoundsAndDefaultLimit(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for i := range 21 {
		id := string(rune('a' + i))
		if err := s.Record(t.Context(), Record{ID: id, LoopID: id, Timestamp: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		name  string
		start time.Time
		end   time.Time
		count int
	}{
		{"inclusive start exclusive end", base, base.Add(time.Second), 1},
		{"fractional start excludes stored earlier second", base.Add(time.Nanosecond), base.Add(2 * time.Second), 1},
		{"fractional end includes stored earlier second", base, base.Add(time.Nanosecond), 1},
		{"empty fractional window", base.Add(time.Nanosecond), base.Add(2 * time.Nanosecond), 0},
		{"offset normalization", base.In(time.FixedZone("west", -5*60*60)), base.Add(time.Second), 1},
		{"zero start means all retained history", time.Time{}, base.Add(time.Minute), 21},
	} {
		t.Run(tt.name, func(t *testing.T) {
			report, err := s.Report(t.Context(), ReportOptions{Start: tt.start, End: tt.end, GroupBy: "loop"})
			if err != nil {
				t.Fatal(err)
			}
			if report.Summary.TotalRecords != tt.count || report.MatchedGroups != tt.count || len(report.Groups) != min(tt.count, 20) || report.Truncated != (tt.count > 20) {
				t.Fatalf("bounded report = %+v; want %d total records/groups", report, tt.count)
			}
		})
	}
}

func TestReportRejectsInvalidOptionsBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, opts := range []ReportOptions{
		{},
		{Start: now, End: now},
		{Start: now, End: now.Add(-time.Second)},
		{End: now, LoopID: "id", LoopName: "name"},
		{End: now, GroupBy: "model; DROP TABLE usage_records"},
		{End: now, Limit: -1},
		{End: now, Limit: 101},
	} {
		report, err := s.Report(t.Context(), opts)
		if report != nil || err == nil || strings.Contains(err.Error(), "database") {
			t.Errorf("invalid options %+v returned %+v, %v", opts, report, err)
		}
	}
	report, err := s.Report(t.Context(), ReportOptions{End: now})
	if report != nil || err == nil || !strings.Contains(err.Error(), "database is closed") {
		t.Fatalf("closed database = %+v, %v", report, err)
	}
}

func TestReportCancellationAndBadAggregateReturnNoPartialReport(t *testing.T) {
	t.Parallel()
	t.Run("canceled caller", func(t *testing.T) {
		s := testStore(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		report, err := s.Report(ctx, ReportOptions{End: time.Now()})
		if report != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled report = %+v, %v", report, err)
		}
	})
	t.Run("waiting for connection", func(t *testing.T) {
		s := testStore(t)
		s.db.SetMaxOpenConns(1)
		conn, err := s.db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
		defer cancel()
		report, err := s.Report(ctx, ReportOptions{End: time.Now(), GroupBy: "loop"})
		if report != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked report = %+v, %v", report, err)
		}
	})
	t.Run("invalid aggregate conversion", func(t *testing.T) {
		s := testStore(t)
		if err := s.Record(t.Context(), Record{ID: "broken"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE usage_records SET input_tokens = 1.5 WHERE id = 'broken'`); err != nil {
			t.Fatal(err)
		}
		report, err := s.Report(t.Context(), ReportOptions{End: time.Now().Add(time.Second), GroupBy: "loop"})
		if report != nil || err == nil || !strings.Contains(err.Error(), "usage report totals") {
			t.Fatalf("bad aggregate = %+v, %v", report, err)
		}
	})
	t.Run("invalid later group discards earlier groups", func(t *testing.T) {
		s := testStore(t)
		for _, rec := range []Record{
			{ID: "a", LoopID: "a", InputTokens: 10, CostUSD: 3},
			{ID: "b", LoopID: "b", CostUSD: 2},
			{ID: "c", LoopID: "c", CostUSD: 1},
		} {
			if err := s.Record(t.Context(), rec); err != nil {
				t.Fatal(err)
			}
		}
		// The total is the valid integer 12, but the second group's fractional
		// counter cannot be decoded. No successful prefix may escape.
		if _, err := s.db.Exec(`UPDATE usage_records SET input_tokens = CASE id WHEN 'b' THEN 1.5 WHEN 'c' THEN 0.5 ELSE input_tokens END`); err != nil {
			t.Fatal(err)
		}
		report, err := s.Report(t.Context(), ReportOptions{End: time.Now().Add(time.Second), GroupBy: "loop"})
		if report != nil || err == nil || !strings.Contains(err.Error(), "scan usage report group") {
			t.Fatalf("bad later group = %+v, %v", report, err)
		}
	})
}
