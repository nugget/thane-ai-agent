package introspection

import (
	"encoding/json"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
)

// The spend section shares the ambient panel's small context budget. Keep
// accounting figures and canonical IDs exact, dropping whole loop rows when
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

// SpendComparison compares recorded costs only when both windows contain
// records and every record has known pricing. Reason explains an unavailable
// comparison or an undefined percentage over a zero prior cost. This is not
// an invoice comparison or evidence of complete provider usage reporting.
type SpendComparison struct {
	Status            string   `json:"status"`
	Reason            string   `json:"reason,omitempty"`
	CostChangeUSD     *float64 `json:"cost_change_usd"`
	CostChangePercent *float64 `json:"cost_change_percent"`
}

// SpendLoops contains at most three directly attributed loops from the recent
// window, ordered by recorded cost descending then ID. Matched counts all
// known loop IDs, independent of the displayed rows and unattributed records.
type SpendLoops struct {
	Matched   int         `json:"matched"`
	Returned  int         `json:"returned"`
	Truncated bool        `json:"truncated"`
	Loops     []SpendLoop `json:"loops"`
}

// SpendLoop preserves the canonical loop ID for cost_summary follow-up. The
// name is the latest captured label in the selected window, not live state.
// LoopNameTruncated marks a shortened display label; LoopID is never shortened.
type SpendLoop struct {
	LoopID            string        `json:"loop_id"`
	LoopName          string        `json:"loop_name"`
	LoopNameTruncated bool          `json:"loop_name_truncated,omitempty"`
	Summary           usage.Summary `json:"summary"`
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
	view.TopLoops = &SpendLoops{
		Matched: snapshot.Last24Hours.MatchedGroups, Truncated: snapshot.Last24Hours.Truncated,
		Loops: []SpendLoop{},
	}
	for _, group := range snapshot.Last24Hours.Groups[:min(3, len(snapshot.Last24Hours.Groups))] {
		name := clipUTF8(group.LoopName, 96)
		view.TopLoops.Loops = append(view.TopLoops.Loops, SpendLoop{
			LoopID: group.Key, LoopName: name, LoopNameTruncated: name != group.LoopName, Summary: group.Summary,
		})
	}
	view.Notes = []string{
		"Stored estimates for provider-reported usage only, including reported usage on failed calls. Calls without usage and embeddings are absent; no records does not establish zero cost.",
		"Missing prices make spend incomplete; unknown pricing makes coverage uncertain. Unattributed records cannot be assigned to loops. Loop totals exclude descendants.",
		"Windows are [since, until) at the snapshot time. Use cost_summary for fresh details and narrower selections.",
	}
	return boundSpendView(view)
}

func spendWindow(report usage.Report, start, end, now time.Time) *SpendWindow {
	return &SpendWindow{
		Since: promptfmt.FormatDeltaOnly(start, now), Until: promptfmt.FormatDeltaOnly(end, now),
		Summary: report.Summary, Unattributed: report.Unattributed,
	}
}

func compareSpend(current, previous usage.Summary) SpendComparison {
	comparison := SpendComparison{Status: "unavailable"}
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
		data, err := json.Marshal(view)
		if err != nil {
			return SpendView{Status: "unavailable", RefreshError: "Recorded spend could not be encoded; inspect the usage ledger."}
		}
		if len(data) <= maxSpendViewBytes {
			return view
		}
		if len(view.TopLoops.Loops) == 0 {
			return SpendView{Status: "unavailable", RefreshError: "Recorded spend exceeds the panel budget; use cost_summary."}
		}
		view.TopLoops.Loops = view.TopLoops.Loops[:len(view.TopLoops.Loops)-1]
	}
}
