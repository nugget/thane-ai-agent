package introspection

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/usage"
)

func TestSpendHeadlinesIncludeUnattributedAndZeroDollarDemand(t *testing.T) {
	asOf := time.Now().UTC().Truncate(time.Second)
	snapshot := spendViewFixture(asOf)
	snapshot.Last24Hours.Summary = usage.Summary{TotalRecords: 476, TotalCostUSD: 15.49,
		TotalInputTokens: 26400000, PricedRecords: 5, UnpricedRecords: 159, UnknownPricingRecords: 312}
	snapshot.Last24Hours.Unattributed = usage.Summary{TotalRecords: 353, TotalCostUSD: 14.81,
		TotalInputTokens: 800000, UnpricedRecords: 41, UnknownPricingRecords: 312}
	snapshot.Last24Hours.Groups = []usage.ReportGroup{
		{Key: "conditions", LoopName: "conditions", Summary: pricedSpend(5, .68)},
		{Key: "whereabouts", LoopName: "whereabouts", Summary: usage.Summary{TotalRecords: 100, UnpricedRecords: 100, TotalInputTokens: 3200000}},
		{Key: "other", LoopName: "other", Summary: usage.Summary{TotalRecords: 18, UnpricedRecords: 18}},
	}
	snapshot.ByProvider = usage.Report{MatchedGroups: 2, Groups: []usage.ReportGroup{
		{Key: "anthropic", Summary: usage.Summary{TotalRecords: 56, TotalCostUSD: 15.49, TotalInputTokens: 800000, PricedRecords: 5, UnknownPricingRecords: 51}},
		{Key: "ollama", Summary: usage.Summary{TotalRecords: 420, TotalInputTokens: 25600000, UnpricedRecords: 159, UnknownPricingRecords: 261}},
	}}
	snapshot.ByRole = usage.Report{MatchedGroups: 3, Groups: []usage.ReportGroup{
		{Key: "interactive", Summary: snapshot.Last24Hours.Unattributed},
		{Key: "autonomous", Summary: usage.Summary{TotalRecords: 120, TotalCostUSD: .68, TotalInputTokens: 25600000, PricedRecords: 5, UnpricedRecords: 115}},
		{Key: "auxiliary", Summary: usage.Summary{TotalRecords: 3, UnpricedRecords: 3}},
	}}
	view := NewInspector(HealthSources{Spend: &SpendCollector{sample: spendSample{Snapshot: snapshot}}}).spendView(asOf)
	if view.Status != "available" || view.ByProvider.Returned != 2 || view.ByRole.Returned < 2 {
		t.Fatalf("whole-agent headlines lost to loop detail: %+v", view)
	}
	for _, want := range snapshot.ByProvider.Groups {
		found := false
		for _, got := range view.ByProvider.Groups {
			if got.Key == want.Key && got.Summary == want.Summary {
				found = true
			}
		}
		if !found {
			t.Errorf("missing full provider usage %+v", want)
		}
	}
	if got := view.ByRole.Groups[0]; got.Key != "interactive" || got.Summary.TotalCostUSD != 14.81 {
		t.Errorf("interactive spend lost from headline: %+v", got)
	}
	if view.TopLoops.Scope != "direct_calls_with_loop_id" || view.TopLoops.RecordedCostSharePercent == nil || math.Abs(*view.TopLoops.RecordedCostSharePercent-.68/15.49*100) > 1e-9 {
		t.Errorf("loop share excludes unattributed denominator: %+v", view.TopLoops)
	}
	for _, loop := range view.TopLoops.Loops {
		if loop.RecordedCostSharePercent == nil || math.Abs(*loop.RecordedCostSharePercent-loop.Summary.TotalCostUSD/15.49*100) > 1e-9 {
			t.Errorf("loop row share = %+v", loop)
		}
	}
	data, err := json.Marshal(view)
	if err != nil || len(data) > maxSpendViewBytes {
		t.Fatalf("spend budget: %d bytes, %v", len(data), err)
	}
	if view.Comparison.Status != "unavailable" || view.Comparison.RecordedCostChangeUSD == nil || view.Comparison.CostChangePercent != nil {
		t.Errorf("partial recorded difference lost or overstated: %+v", view.Comparison)
	}
}

func TestSpendHeadlinesBoundsRetainTotalsAndExactKeys(t *testing.T) {
	asOf := time.Now().UTC().Truncate(time.Second)
	snapshot := spendViewFixture(asOf)
	hugeKey := strings.Repeat("界<&\n", 5000)
	for _, report := range []*usage.Report{&snapshot.ByProvider, &snapshot.ByRole} {
		report.MatchedGroups = 1
		report.Groups = []usage.ReportGroup{{Key: hugeKey, Summary: pricedSpend(4, 4)}}
	}
	view := NewInspector(HealthSources{Spend: &SpendCollector{sample: spendSample{Snapshot: snapshot}}}).spendView(asOf)
	if view.Status != "available" || view.Last24Hours.Summary != snapshot.Last24Hours.Summary || view.Last24Hours.Unattributed != snapshot.Last24Hours.Unattributed {
		t.Fatalf("large keys changed accounting: %+v", view)
	}
	for _, groups := range []*SpendGroups{view.ByProvider, view.ByRole} {
		if groups.Matched != 1 || groups.Returned != 0 || !groups.Truncated {
			t.Errorf("oversized key should omit entire group: %+v", groups)
		}
	}
	if snapshot.ByProvider.Groups[0].Key != hugeKey || snapshot.ByRole.Groups[0].Key != hugeKey {
		t.Error("bounding mutated shared snapshot")
	}
}

func TestSpendRecordedDeltaAndZeroDenominator(t *testing.T) {
	got := compareSpend(usage.Summary{TotalRecords: 1, UnpricedRecords: 1, TotalCostUSD: 35.72},
		usage.Summary{TotalRecords: 1, UnknownPricingRecords: 1, TotalCostUSD: 30.99})
	if got.RecordedCostChangeUSD == nil || math.Abs(*got.RecordedCostChangeUSD-4.73) > 1e-9 || got.Status != "unavailable" || got.CostChangeUSD != nil || got.CostChangePercent != nil {
		t.Errorf("partial difference mistaken for eligible comparison: %+v", got)
	}
	if recordedCostShare(0, 0) != nil {
		t.Error("zero recorded dollars must leave share undefined")
	}
}
