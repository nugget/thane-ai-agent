package usage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSpendSnapshotWindowsCoverageAndLoopCap(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	asOf := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for _, rec := range []Record{
		{ID: "outside-before", Timestamp: asOf.Add(-48*time.Hour - time.Second), CostUSD: 100},
		{ID: "previous-start", Timestamp: asOf.Add(-48 * time.Hour), CostUSD: 2, PricingStatus: "priced"},
		{ID: "previous-end", Timestamp: asOf.Add(-24*time.Hour - time.Second), LoopID: "old", CostUSD: 3},
		{ID: "current-start", Timestamp: asOf.Add(-24 * time.Hour), LoopID: "a", LoopName: "alpha", CostUSD: 5, PricingStatus: "priced"},
		{ID: "b", Timestamp: asOf.Add(-time.Hour), LoopID: "b", LoopName: "beta", CostUSD: 4, PricingStatus: "priced"},
		{ID: "c", Timestamp: asOf.Add(-time.Hour), LoopID: "c", LoopName: "gamma", CostUSD: 2, PricingStatus: "unpriced"},
		{ID: "d", Timestamp: asOf.Add(-time.Hour), LoopID: "d", LoopName: "delta", CostUSD: 1},
		{ID: "unattributed", Timestamp: asOf.Add(-time.Hour), CostUSD: 10, PricingStatus: "priced"},
		{ID: "outside-end", Timestamp: asOf, LoopID: "future", CostUSD: 200},
		{ID: "outside-after", Timestamp: asOf.Add(time.Second), CostUSD: 300},
	} {
		rec.InputTokens, rec.OutputTokens = 1, 2
		rec.CacheCreationInputTokens, rec.CacheReadInputTokens = 3, 4
		if err := s.Record(t.Context(), rec); err != nil {
			t.Fatal(err)
		}
	}
	// A single whole-second UTC anchor excludes the still-open second even
	// when the caller supplies fractional seconds in another timezone.
	requested := asOf.Add(900 * time.Millisecond).In(time.FixedZone("west", -5*60*60))
	snapshot, err := s.SpendSnapshot(t.Context(), requested)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.AsOf != asOf {
		t.Fatalf("anchor = %s, want %s", snapshot.AsOf, asOf)
	}
	wantCurrent := Summary{TotalRecords: 5, TotalInputTokens: 5, TotalOutputTokens: 10,
		TotalCacheCreationInputTokens: 15, TotalCacheReadInputTokens: 20, TotalCostUSD: 22,
		PricedRecords: 3, UnpricedRecords: 1, UnknownPricingRecords: 1}
	if snapshot.Last24Hours.Summary != wantCurrent {
		t.Errorf("current totals = %+v, want %+v", snapshot.Last24Hours.Summary, wantCurrent)
	}
	wantPrevious := Summary{TotalRecords: 2, TotalInputTokens: 2, TotalOutputTokens: 4,
		TotalCacheCreationInputTokens: 6, TotalCacheReadInputTokens: 8, TotalCostUSD: 5,
		PricedRecords: 1, UnknownPricingRecords: 1}
	if snapshot.Previous24Hours.Summary != wantPrevious {
		t.Errorf("previous totals = %+v, want %+v", snapshot.Previous24Hours.Summary, wantPrevious)
	}
	if snapshot.Last24Hours.Unattributed.TotalRecords != 1 || snapshot.Last24Hours.Unattributed.TotalCostUSD != 10 || snapshot.Last24Hours.Unattributed.PricedRecords != 1 {
		t.Errorf("current unattributed = %+v", snapshot.Last24Hours.Unattributed)
	}
	if snapshot.Previous24Hours.Unattributed.TotalRecords != 1 || snapshot.Previous24Hours.Unattributed.TotalCostUSD != 2 || snapshot.Previous24Hours.Unattributed.PricedRecords != 1 {
		t.Errorf("previous unattributed = %+v", snapshot.Previous24Hours.Unattributed)
	}
	if snapshot.Last24Hours.MatchedGroups != 4 || len(snapshot.Last24Hours.Groups) != 3 || !snapshot.Last24Hours.Truncated {
		t.Fatalf("current group count/cap = %+v", snapshot.Last24Hours)
	}
	for i, want := range []struct {
		id   string
		name string
		cost float64
	}{{"a", "alpha", 5}, {"b", "beta", 4}, {"c", "gamma", 2}} {
		got := snapshot.Last24Hours.Groups[i]
		if got.Key != want.id || got.LoopName != want.name || got.Summary.TotalCostUSD != want.cost {
			t.Errorf("top loop %d = %+v, want %+v", i, got, want)
		}
	}
	if snapshot.Previous24Hours.MatchedGroups != 0 || len(snapshot.Previous24Hours.Groups) != 0 || snapshot.Previous24Hours.Truncated {
		t.Errorf("previous window should have totals only: %+v", snapshot.Previous24Hours)
	}
}

func TestSpendSnapshotEmptyAndExplicitlyFreeRemainDistinct(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	asOf := time.Now().UTC().Truncate(time.Second)
	if err := s.Record(t.Context(), Record{Timestamp: asOf.Add(-time.Minute), LoopID: "free", PricingStatus: "priced"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.SpendSnapshot(t.Context(), asOf)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Last24Hours.Summary.TotalCostUSD != 0 || snapshot.Last24Hours.Summary.TotalRecords != 1 || snapshot.Last24Hours.Summary.PricedRecords != 1 {
		t.Errorf("explicitly free current record = %+v", snapshot.Last24Hours)
	}
	if snapshot.Previous24Hours.Summary != (Summary{}) || snapshot.Previous24Hours.Unattributed != (Summary{}) || snapshot.Previous24Hours.Groups == nil {
		t.Errorf("empty previous window = %+v", snapshot.Previous24Hours)
	}
}

func TestSpendSnapshotProviderAndRoleIncludeUnattributedAndZeroRateUsage(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	asOf := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for _, rec := range []Record{
		{ID: "interactive", Provider: "anthropic", Role: "interactive", InputTokens: 100, CostUSD: 15, PricingStatus: "priced"},
		{ID: "paid-loop", LoopID: "hor", Provider: "anthropic", Role: "autonomous", InputTokens: 10, CostUSD: 1, PricingStatus: "priced"},
		{ID: "local-loop", LoopID: "local", Provider: "ollama", Role: "autonomous", InputTokens: 25_600_000, PricingStatus: "priced"},
		{ID: "legacy-unknown", CostUSD: 2},
	} {
		rec.Timestamp = asOf.Add(-time.Hour)
		if err := s.Record(t.Context(), rec); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := s.SpendSnapshot(t.Context(), asOf)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		report Report
		keys   []string
		costs  []float64
		tokens []int64
	}{
		{"provider", snapshot.ByProvider, []string{"anthropic", "", "ollama"}, []float64{16, 2, 0}, []int64{110, 0, 25_600_000}},
		{"role", snapshot.ByRole, []string{"interactive", "", "autonomous"}, []float64{15, 2, 1}, []int64{100, 0, 25_600_010}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.report.Summary != snapshot.Last24Hours.Summary || tt.report.Unattributed != snapshot.Last24Hours.Unattributed {
				t.Errorf("dimension report changed totals/coverage: %+v", tt.report)
			}
			if tt.report.Summary.TotalCostUSD != 18 || tt.report.Unattributed.TotalCostUSD != 17 || tt.report.MatchedGroups != 3 || len(tt.report.Groups) != 3 || tt.report.Truncated {
				t.Fatalf("whole-agent grouped report = %+v", tt.report)
			}
			for i, key := range tt.keys {
				group := tt.report.Groups[i]
				if group.Key != key || group.Summary.TotalCostUSD != tt.costs[i] || group.Summary.TotalInputTokens != tt.tokens[i] {
					t.Errorf("group %d = %+v", i, group)
				}
				if group.Summary.PricedRecords+group.Summary.UnpricedRecords+group.Summary.UnknownPricingRecords != group.Summary.TotalRecords {
					t.Errorf("group coverage incomplete: %+v", group)
				}
			}
		})
	}
	// A fourth key in each dimension is omitted by the shared three-row cap,
	// while every report still includes its usage in the complete totals.
	if err := s.Record(t.Context(), Record{ID: "fourth", Timestamp: asOf.Add(-time.Minute), Provider: "other", Role: "auxiliary", CostUSD: 3, PricingStatus: "unpriced"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = s.SpendSnapshot(t.Context(), asOf)
	if err != nil {
		t.Fatal(err)
	}
	for _, report := range []Report{snapshot.ByProvider, snapshot.ByRole} {
		if report.Summary != snapshot.Last24Hours.Summary || report.Unattributed != snapshot.Last24Hours.Unattributed || report.Summary.TotalCostUSD != 21 || report.MatchedGroups != 4 || len(report.Groups) != 3 || !report.Truncated {
			t.Errorf("capped dimension report = %+v", report)
		}
	}
}

func TestSpendSnapshotFailureReturnsNoPartialWindows(t *testing.T) {
	t.Parallel()
	t.Run("invalid anchor before database access", func(t *testing.T) {
		s := testStore(t)
		if err := s.db.Close(); err != nil {
			t.Fatal(err)
		}
		snapshot, err := s.SpendSnapshot(t.Context(), time.Time{})
		if snapshot != nil || err == nil || !strings.Contains(err.Error(), "as_of") {
			t.Fatalf("invalid anchor = %+v, %v", snapshot, err)
		}
		snapshot, err = s.SpendSnapshot(t.Context(), time.Now())
		if snapshot != nil || err == nil || !strings.Contains(err.Error(), "database is closed") {
			t.Fatalf("closed database = %+v, %v", snapshot, err)
		}
	})
	t.Run("canceled caller", func(t *testing.T) {
		s := testStore(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		snapshot, err := s.SpendSnapshot(ctx, time.Now())
		if snapshot != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled read = %+v, %v", snapshot, err)
		}
	})
	t.Run("cancellation waiting for connection", func(t *testing.T) {
		s := testStore(t)
		s.db.SetMaxOpenConns(1)
		conn, err := s.db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
		defer cancel()
		snapshot, err := s.SpendSnapshot(ctx, time.Now())
		if snapshot != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked read = %+v, %v", snapshot, err)
		}
	})
	t.Run("previous window failure discards completed current window", func(t *testing.T) {
		s := testStore(t)
		asOf := time.Now().UTC().Truncate(time.Second)
		for _, rec := range []Record{
			{ID: "current", Timestamp: asOf.Add(-time.Hour), LoopID: "a", CostUSD: 5},
			{ID: "broken-previous", Timestamp: asOf.Add(-25 * time.Hour)},
		} {
			if err := s.Record(t.Context(), rec); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.Exec(`UPDATE usage_records SET input_tokens = 1.5 WHERE id = 'broken-previous'`); err != nil {
			t.Fatal(err)
		}
		snapshot, err := s.SpendSnapshot(t.Context(), asOf)
		if snapshot != nil || err == nil || !strings.Contains(err.Error(), "read previous 24 hours") {
			t.Fatalf("partial snapshot = %+v, %v", snapshot, err)
		}
	})
	for _, dimension := range []string{"provider", "role"} {
		t.Run(dimension+" failure discards completed windows", func(t *testing.T) {
			s := testStore(t)
			asOf := time.Now().UTC().Truncate(time.Second)
			for _, id := range []string{"a", "b"} {
				rec := Record{ID: id, Timestamp: asOf.Add(-time.Hour), LoopID: "same", Provider: "same", Role: "same"}
				if dimension == "provider" {
					rec.Provider = id
				} else {
					rec.Role = id
				}
				if err := s.Record(t.Context(), rec); err != nil {
					t.Fatal(err)
				}
			}
			// Whole-window, unattributed, and loop totals are integers, but the
			// selected dimension has fractional counters that cannot decode.
			if _, err := s.db.Exec(`UPDATE usage_records SET input_tokens = CASE id WHEN 'a' THEN 1.5 ELSE 0.5 END`); err != nil {
				t.Fatal(err)
			}
			snapshot, err := s.SpendSnapshot(t.Context(), asOf)
			if snapshot != nil || err == nil || !strings.Contains(err.Error(), "read last 24 hours by "+dimension) {
				t.Fatalf("partial snapshot = %+v, %v", snapshot, err)
			}
		})
	}
}
