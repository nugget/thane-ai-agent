package introspection

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

func pricedSpend(records int, cost float64) usage.Summary {
	return usage.Summary{TotalRecords: records, PricedRecords: records, TotalCostUSD: cost}
}

func TestSpendComparisonCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, reason          string
		current, previous     usage.Summary
		available, percentage bool
		delta, percent        float64
	}{
		{name: "increase", current: pricedSpend(2, 3), previous: pricedSpend(1, 2), available: true, percentage: true, delta: 1, percent: 50},
		{name: "decrease", current: pricedSpend(1, 1), previous: pricedSpend(1, 2), available: true, percentage: true, delta: -1, percent: -50},
		{name: "zero configured price", current: pricedSpend(1, 0), previous: pricedSpend(1, 2), available: true, percentage: true, delta: -2, percent: -100},
		{name: "prior configured free", current: pricedSpend(1, 2), previous: pricedSpend(1, 0), available: true, delta: 2, reason: "zero_prior_cost"},
		{name: "both configured free", current: pricedSpend(1, 0), previous: pricedSpend(1, 0), available: true, reason: "zero_prior_cost"},
		{name: "empty prior", current: pricedSpend(1, 2), reason: "empty_window"},
		{name: "empty current", previous: pricedSpend(1, 2), reason: "empty_window"},
		{name: "current missing", current: usage.Summary{TotalRecords: 1, UnpricedRecords: 1}, previous: pricedSpend(1, 2), reason: "missing_prices"},
		{name: "prior missing", current: pricedSpend(1, 2), previous: usage.Summary{TotalRecords: 1, UnpricedRecords: 1}, reason: "missing_prices"},
		{name: "historical cost retained", current: usage.Summary{TotalRecords: 1, TotalCostUSD: 3, UnknownPricingRecords: 1}, previous: pricedSpend(1, 2), reason: "unknown_pricing"},
		{name: "prior unknown", current: pricedSpend(1, 2), previous: usage.Summary{TotalRecords: 1, TotalCostUSD: 3, UnknownPricingRecords: 1}, reason: "unknown_pricing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := compareSpend(tc.current, tc.previous)
			if (got.Status == "available") != tc.available || got.Reason != tc.reason {
				t.Fatalf("comparison = %+v", got)
			}
			if (got.CostChangeUSD != nil) != tc.available || (got.CostChangePercent != nil) != tc.percentage {
				t.Fatalf("comparison availability = %+v", got)
			}
			wantRecorded := tc.current.TotalRecords > 0 && tc.previous.TotalRecords > 0
			if (got.RecordedCostChangeUSD != nil) != wantRecorded {
				t.Fatalf("recorded change availability = %+v", got)
			}
			if wantRecorded && *got.RecordedCostChangeUSD != tc.current.TotalCostUSD-tc.previous.TotalCostUSD {
				t.Errorf("recorded change lost partial estimates: %+v", got)
			}
			if got.CostChangeUSD != nil && *got.CostChangeUSD != tc.delta {
				t.Errorf("dollar change = %v, want %v", *got.CostChangeUSD, tc.delta)
			}
			if got.CostChangePercent != nil && *got.CostChangePercent != tc.percent {
				t.Errorf("percentage = %v, want %v", *got.CostChangePercent, tc.percent)
			}
		})
	}
}

func spendViewFixture(asOf time.Time) *usage.SpendSnapshot {
	return &usage.SpendSnapshot{
		AsOf: asOf,
		Last24Hours: usage.Report{
			Summary: pricedSpend(4, 4), Unattributed: pricedSpend(1, 1),
			MatchedGroups: 3, Groups: []usage.ReportGroup{
				{Key: "loop-a", LoopName: "worker", Summary: pricedSpend(1, 1)},
				{Key: "loop-b", LoopName: "worker", Summary: pricedSpend(1, 1)},
				{Key: "loop-c", LoopName: "other", Summary: pricedSpend(1, 1)},
			},
		},
		Previous24Hours: usage.Report{Summary: pricedSpend(2, 2), Unattributed: pricedSpend(1, 1)},
	}
}

func TestSpendViewStateAndAge(t *testing.T) {
	asOf := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, state string
		sample      spendSample
		age         time.Duration
	}{
		{name: "not started", state: "pending"},
		{name: "first collection pending", state: "pending", sample: spendSample{Refreshing: true, LastAttempt: asOf}},
		{name: "first failure", state: "unavailable", sample: spendSample{LastError: "database unavailable", LastAttempt: asOf}},
		{name: "fresh", state: "available", age: time.Minute, sample: spendSample{Snapshot: spendViewFixture(asOf), LastAttempt: asOf}},
		{name: "expired", state: "stale", age: 6 * time.Minute, sample: spendSample{Snapshot: spendViewFixture(asOf), LastAttempt: asOf}},
		{name: "refresh pending", state: "stale", age: 6 * time.Minute, sample: spendSample{Snapshot: spendViewFixture(asOf), LastAttempt: asOf.Add(5 * time.Minute), Refreshing: true}},
		{name: "failed refresh retains window", state: "stale", age: 6 * time.Minute, sample: spendSample{Snapshot: spendViewFixture(asOf), LastError: "refresh timed out", LastAttempt: asOf.Add(5 * time.Minute)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector := &SpendCollector{sample: tc.sample, query: func(context.Context, time.Time) (*usage.SpendSnapshot, error) {
				t.Fatal("render queried the ledger")
				return nil, nil
			}}
			insp := NewInspector(HealthSources{Spend: collector})
			now := asOf.Add(tc.age)
			got := insp.spendView(now)
			if got.Status != tc.state || got.Refreshing != tc.sample.Refreshing || got.RefreshError != tc.sample.LastError {
				t.Fatalf("state metadata = %+v", got)
			}
			if tc.sample.Snapshot == nil {
				if got.Last24Hours != nil || got.Previous24Hours != nil || got.TopLoops != nil || got.Comparison != nil {
					t.Fatalf("unavailable data became a measurement: %+v", got)
				}
				return
			}
			if got.SampledAgo != promptfmt.FormatDeltaOnly(asOf, now) || got.Last24Hours.Until != got.SampledAgo || got.Previous24Hours.Until != got.Last24Hours.Since {
				t.Errorf("cached windows drifted: %+v", got)
			}
			if got.Last24Hours.Summary != tc.sample.Snapshot.Last24Hours.Summary || got.Previous24Hours.Unattributed != tc.sample.Snapshot.Previous24Hours.Unattributed {
				t.Error("projection lost complete totals or unattributed coverage")
			}
			if got.TopLoops.Matched != 3 || got.TopLoops.Returned != 3 || got.TopLoops.Truncated || got.Comparison.Status != "available" {
				t.Errorf("unexpected grouping/comparison: %+v", got)
			}
			if !collector.snapshot().Snapshot.AsOf.Equal(asOf) {
				t.Error("render mutated shared snapshot anchor")
			}
		})
	}
	if got := NewInspector(HealthSources{}).spendView(asOf); got.Status != "unavailable" || got.Last24Hours != nil {
		t.Errorf("unwired collector must be unavailable: %+v", got)
	}
	collector := &SpendCollector{sample: spendSample{Snapshot: &usage.SpendSnapshot{AsOf: asOf}}}
	got := NewInspector(HealthSources{Spend: collector}).spendView(asOf)
	if got.Status != "available" || got.Last24Hours == nil || got.Last24Hours.Summary.TotalRecords != 0 || got.Comparison.Reason != "empty_window" {
		t.Errorf("successful empty ledger must remain a recorded zero, not unavailable: %+v", got)
	}
}

func TestSpendViewBoundsPreserveAccountingAndIDs(t *testing.T) {
	asOf := time.Now().UTC().Truncate(time.Second)
	for _, largeID := range []bool{false, true} {
		snapshot := spendViewFixture(asOf)
		for index := range snapshot.Last24Hours.Groups {
			snapshot.Last24Hours.Groups[index].LoopName = strings.Repeat("界<&\n", 2000)
			if largeID {
				snapshot.Last24Hours.Groups[index].Key = strings.Repeat("<&", 5000)
			}
		}
		collector := &SpendCollector{sample: spendSample{Snapshot: snapshot, LastError: strings.Repeat("界<&\n", 2000)}}
		got := NewInspector(HealthSources{Spend: collector}).spendView(asOf)
		data, err := json.Marshal(got)
		if err != nil || !json.Valid(data) || !utf8.Valid(data) || len(data) > maxSpendViewBytes {
			t.Fatalf("invalid or oversized spend payload: %d bytes, %v", len(data), err)
		}
		if got.Last24Hours == nil || got.Last24Hours.Summary != snapshot.Last24Hours.Summary || got.TopLoops.Matched != 3 || got.TopLoops.Returned != len(got.TopLoops.Loops) {
			t.Fatalf("size cap changed accounting: %+v", got)
		}
		if largeID && (!got.TopLoops.Truncated || got.TopLoops.Returned != 0) {
			t.Errorf("oversized IDs should omit whole groups: %+v", got.TopLoops)
		}
		for index, row := range got.TopLoops.Loops {
			if !row.LoopNameTruncated || row.LoopID != snapshot.Last24Hours.Groups[index].Key {
				t.Errorf("lost name truncation or canonical ID: %+v", row)
			}
		}
		if collector.snapshot().Snapshot.Last24Hours.Groups[0].LoopName != strings.Repeat("界<&\n", 2000) {
			t.Error("projection mutated immutable cached names")
		}
	}
}

func TestSpendViewInvalidNumbersCannotPoisonPanel(t *testing.T) {
	asOf := time.Now().UTC().Truncate(time.Second)
	snapshot := spendViewFixture(asOf)
	snapshot.Last24Hours.Summary.TotalCostUSD = math.Inf(1)
	view := NewInspector(HealthSources{Spend: &SpendCollector{sample: spendSample{Snapshot: snapshot}}}).spendView(asOf)
	if view.Status != "unavailable" || view.Last24Hours != nil || view.RefreshError == "" {
		t.Errorf("non-JSON number escaped: %+v", view)
	}
	if _, err := json.Marshal(view); err != nil {
		t.Fatalf("fallback is not JSON: %v", err)
	}
}

func TestSpendToolAndPanelShareSnapshotWithoutAlarms(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	collector := &SpendCollector{sample: spendSample{Snapshot: spendViewFixture(now.Add(-time.Minute)), LastError: "refresh failed"}}
	insp := NewInspector(HealthSources{Spend: collector})
	insp.now = func() time.Time { return now }
	toolResult, err := NewTools(ToolDeps{Inspector: insp}).handleSystemHealth(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var toolPayload map[string]any
	if err := json.Unmarshal([]byte(toolResult), &toolPayload); err != nil {
		t.Fatal(err)
	}
	panel, err := NewPanelProvider(insp, nil, nil).TagContext(t.Context(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatal(err)
	}
	panelPayload := panelJSON(t, panel)
	if !reflect.DeepEqual(toolPayload["spend"], panelPayload["spend"]) {
		t.Fatalf("tool and context disagree: %v / %v", toolPayload["spend"], panelPayload["spend"])
	}
	if !strings.Contains(panel, "cached ledger snapshot") || len(insp.Health(t.Context()).Degraded()) != 0 {
		t.Error("spend metadata should describe cached evidence without inventing an alarm")
	}
	if !strings.Contains(toolResult, `"status":"stale"`) || !strings.Contains(toolResult, `"sampled_ago":"-60s"`) {
		t.Errorf("shared surface lost freshness metadata: %s", toolResult)
	}
}
