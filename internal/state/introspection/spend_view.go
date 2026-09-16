package introspection

import (
	"encoding/json"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
)

// The spend section shares the ambient panel's small context budget. Keep
// accounting figures and canonical keys exact, dropping whole rows when
// their encoded size is too large. Descriptive names alone may be clipped.
const maxSpendViewBytes = 4 * 1024

// SpendView is the bounded recorded-spend section shared by system_health and
// the metacognitive panel. Status is pending before the first collection,
// unavailable when unwired or the first collection fails, available for a
// fresh successful snapshot, or stale after a refresh failure or five minutes.
// A stale view retains its original windows and age. Missing windows are not
// zero measurements. Neither spend nor collection failure creates a health
// alarm: the model interprets the evidence against its own baselines.
type SpendView struct {
	Status          string           `json:"status"`
	SampledAgo      string           `json:"sampled_ago,omitempty"`
	LastAttemptAgo  string           `json:"last_attempt_ago,omitempty"`
	Refreshing      bool             `json:"refreshing"`
	RefreshError    string           `json:"refresh_error,omitempty"`
	Last24Hours     *SpendWindow     `json:"last_24h,omitempty"`
	Previous24Hours *SpendWindow     `json:"previous_24h,omitempty"`
	Comparison      *SpendComparison `json:"comparison,omitempty"`
	ByProvider      *SpendGroups     `json:"by_provider,omitempty"`
	ByRole          *SpendGroups     `json:"by_role,omitempty"`
	TopLoops        *SpendLoops      `json:"top_loops,omitempty"`
	Notes           []string         `json:"notes,omitempty"`
}

// SpendWindow describes one half-open ledger window, with bounds relative to
// render time. Summary covers every matching record. Unattributed is the
// subset without a captured loop ID, including historical and non-loop work.
type SpendWindow struct {
	Since        string        `json:"since"`
	Until        string        `json:"until"`
	Summary      usage.Summary `json:"summary"`
	Unattributed usage.Summary `json:"unattributed"`
}

// SpendComparison exposes RecordedCostChangeUSD whenever both windows contain
// records, even with incomplete pricing. That difference can reflect coverage
// changes and is not a bound on the real change. Status, CostChangeUSD, and
// CostChangePercent describe the stricter comparison requiring fully priced
// windows. Reason explains ineligibility or an undefined zero-baseline percent.
// Neither comparison establishes invoice completeness.
type SpendComparison struct {
	Status                string   `json:"status"`
	Reason                string   `json:"reason,omitempty"`
	RecordedCostChangeUSD *float64 `json:"recorded_cost_change_usd"`
	CostChangeUSD         *float64 `json:"cost_change_usd"`
	CostChangePercent     *float64 `json:"cost_change_percent"`
}

// SpendGroups contains up to three groups of recent usage by provider or role,
// including records without loop IDs. Keys are exact; a blank key means the
// dimension was not captured. Matched counts all groups before either cap.
type SpendGroups struct {
	Matched   int                 `json:"matched"`
	Returned  int                 `json:"returned"`
	Truncated bool                `json:"truncated"`
	Groups    []usage.ReportGroup `json:"groups"`
}

// SpendLoops contains at most three directly attributed loops from the recent
// window, ordered by recorded cost descending then ID. Matched counts all
// known loop IDs, independent of the displayed rows and unattributed records.
// RecordedCostSharePercent includes all known loop IDs, even omitted rows,
// divided by all recent recorded dollars. It is null for a zero denominator.
type SpendLoops struct {
	Scope                    string      `json:"scope"`
	RecordedCostSharePercent *float64    `json:"recorded_cost_share_percent"`
	Matched                  int         `json:"matched"`
	Returned                 int         `json:"returned"`
	Truncated                bool        `json:"truncated"`
	Loops                    []SpendLoop `json:"loops"`
}

// SpendLoop preserves the canonical loop ID for cost_summary follow-up. The
// name is the latest captured label in the selected window, not live state.
// LoopNameTruncated marks a shortened display label; LoopID is never shortened.
// RecordedCostSharePercent uses all recent recorded dollars as its denominator,
// including unattributed costs, and is null when that denominator is zero.
type SpendLoop struct {
	LoopID                   string        `json:"loop_id"`
	LoopName                 string        `json:"loop_name"`
	LoopNameTruncated        bool          `json:"loop_name_truncated,omitempty"`
	Summary                  usage.Summary `json:"summary"`
	RecordedCostSharePercent *float64      `json:"recorded_cost_share_percent"`
}

func (i *Inspector) spendView(now time.Time) SpendView {
	if i.src.Spend == nil {
		return SpendView{Status: "unavailable", RefreshError: "Usage collector is not configured."}
	}
	sample := i.src.Spend.snapshot()
	view := SpendView{Status: "pending", Refreshing: sample.Refreshing, RefreshError: clipUTF8(sample.LastError, 192)}
	if !sample.LastAttempt.IsZero() {
		view.LastAttemptAgo = promptfmt.FormatDeltaOnly(sample.LastAttempt, now)
	}
	if sample.Snapshot == nil {
		if sample.LastError != "" {
			view.Status = "unavailable"
		}
		return view
	}
	snapshot := sample.Snapshot
	view.Status = "available"
	if sample.LastError != "" || now.Sub(snapshot.AsOf) >= spendRefreshInterval {
		view.Status = "stale"
	}
	view.SampledAgo = promptfmt.FormatDeltaOnly(snapshot.AsOf, now)
	view.Last24Hours = spendWindow(snapshot.Last24Hours, snapshot.AsOf.Add(-24*time.Hour), snapshot.AsOf, now)
	view.Previous24Hours = spendWindow(snapshot.Previous24Hours, snapshot.AsOf.Add(-48*time.Hour), snapshot.AsOf.Add(-24*time.Hour), now)
	comparison := compareSpend(snapshot.Last24Hours.Summary, snapshot.Previous24Hours.Summary)
	view.Comparison = &comparison
	view.ByProvider = spendGroups(snapshot.ByProvider)
	view.ByRole = spendGroups(snapshot.ByRole)
	totalCost := snapshot.Last24Hours.Summary.TotalCostUSD
	view.TopLoops = &SpendLoops{
		Scope:                    "direct_calls_with_loop_id",
		RecordedCostSharePercent: recordedCostShare(totalCost-snapshot.Last24Hours.Unattributed.TotalCostUSD, totalCost),
		Matched:                  snapshot.Last24Hours.MatchedGroups, Truncated: snapshot.Last24Hours.Truncated,
		Loops: []SpendLoop{},
	}
	for _, group := range snapshot.Last24Hours.Groups[:min(3, len(snapshot.Last24Hours.Groups))] {
		name := clipUTF8(group.LoopName, 96)
		view.TopLoops.Loops = append(view.TopLoops.Loops, SpendLoop{
			LoopID: group.Key, LoopName: name, LoopNameTruncated: name != group.LoopName, Summary: group.Summary,
			RecordedCostSharePercent: recordedCostShare(group.Summary.TotalCostUSD, totalCost),
		})
	}
	view.Notes = []string{
		"Provider and role groups cover last_24h, including missing loop IDs. Loop shares use all recorded dollars; direct calls exclude descendants.",
		"Priced zero means configured $0 API cost, not zero resource demand. Missing prices and unknown coverage limit totals; a partial recorded delta may reflect coverage changes, not spend changes.",
		"Windows are [since, until). Reported usage on failures is included; calls without usage and embeddings are absent. Use cost_summary for fresh detail.",
	}
	return boundSpendView(view)
}

func spendGroups(report usage.Report) *SpendGroups {
	return &SpendGroups{Matched: report.MatchedGroups, Truncated: report.Truncated,
		Groups: append([]usage.ReportGroup{}, report.Groups[:min(3, len(report.Groups))]...)}
}

func recordedCostShare(cost, total float64) *float64 {
	if total == 0 {
		return nil
	}
	percent := cost / total * 100
	return &percent
}

func spendWindow(report usage.Report, start, end, now time.Time) *SpendWindow {
	return &SpendWindow{
		Since: promptfmt.FormatDeltaOnly(start, now), Until: promptfmt.FormatDeltaOnly(end, now),
		Summary: report.Summary, Unattributed: report.Unattributed,
	}
}

func compareSpend(current, previous usage.Summary) SpendComparison {
	comparison := SpendComparison{Status: "unavailable"}
	if current.TotalRecords > 0 && previous.TotalRecords > 0 {
		delta := current.TotalCostUSD - previous.TotalCostUSD
		comparison.RecordedCostChangeUSD = &delta
	}
	switch {
	case current.TotalRecords == 0 || previous.TotalRecords == 0:
		comparison.Reason = "empty_window"
	case current.UnpricedRecords > 0 || previous.UnpricedRecords > 0:
		comparison.Reason = "missing_prices"
	case current.PricedRecords != current.TotalRecords || previous.PricedRecords != previous.TotalRecords:
		comparison.Reason = "unknown_pricing"
	default:
		comparison.Status = "available"
		delta := current.TotalCostUSD - previous.TotalCostUSD
		comparison.CostChangeUSD = &delta
		if previous.TotalCostUSD == 0 {
			comparison.Reason = "zero_prior_cost"
		} else {
			percent := delta / previous.TotalCostUSD * 100
			comparison.CostChangePercent = &percent
		}
	}
	return comparison
}

func boundSpendView(view SpendView) SpendView {
	for {
		view.TopLoops.Returned = len(view.TopLoops.Loops)
		view.TopLoops.Truncated = view.TopLoops.Truncated || view.TopLoops.Returned < view.TopLoops.Matched
		for _, groups := range []*SpendGroups{view.ByProvider, view.ByRole} {
			groups.Returned = len(groups.Groups)
			groups.Truncated = groups.Truncated || groups.Returned < groups.Matched
		}
		data, err := json.Marshal(view)
		if err != nil {
			return SpendView{Status: "unavailable", RefreshError: "Recorded spend could not be encoded; inspect the usage ledger."}
		}
		if len(data) <= maxSpendViewBytes {
			return view
		}
		// Spend headlines take precedence over instance drilldown. Trim the
		// longer headline list next, preserving both dimensions when possible.
		if len(view.TopLoops.Loops) > 0 {
			view.TopLoops.Loops = view.TopLoops.Loops[:len(view.TopLoops.Loops)-1]
			continue
		}
		groups := view.ByProvider
		if len(view.ByRole.Groups) > len(groups.Groups) {
			groups = view.ByRole
		}
		if len(groups.Groups) > 0 {
			groups.Groups = groups.Groups[:len(groups.Groups)-1]
			continue
		}
		return SpendView{Status: "unavailable", RefreshError: "Recorded spend exceeds the panel budget; use cost_summary."}
	}
}
