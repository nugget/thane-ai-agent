package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
)

const maxCostSummaryBytes = 16 * 1024

func (r *Registry) registerCostSummary() {
	if r.usageStore == nil {
		return
	}
	r.Register(&Tool{
		Name:        "cost_summary",
		Description: "Query recorded token usage and estimated API costs as JSON. Start with group_by=provider or role for whole-agent spend, including records without loop IDs. Use group_by=loop_name to combine captured names across restarts, group_by=loop for instance IDs, or loop_id=self for your direct calls. Loop selectors exclude descendants. Totals cover all matching records even when groups are truncated. Pricing distinguishes configured rates (including zero), missing prices, and unknown historical coverage. A $0 estimate does not establish zero resource use.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"period": map[string]any{
					"type": "string", "enum": []string{"today", "yesterday", "week", "month", "all"},
					"description": "Named time window (default today). Days use server local time; week and month are trailing windows. Cannot combine with since or until.",
				},
				"since": map[string]any{
					"type": "string", "maxLength": 128,
					"description": "Inclusive custom start: signed offset such as -24h or -7d, or RFC3339 timestamp. Omit period when using this.",
				},
				"until": map[string]any{
					"type": "string", "maxLength": 128,
					"description": "Exclusive custom end: signed offset or RFC3339 timestamp (default now). Requires since.",
				},
				"loop_id": map[string]any{
					"type": "string", "maxLength": 256,
					"description": "Exact loop instance ID, including completed loops, or self for the calling loop. Cannot combine with loop_name. Omit both selectors for all recorded usage.",
				},
				"loop_name": map[string]any{
					"type": "string", "maxLength": 256,
					"description": "Exact captured name, including matching records without an instance ID. May match multiple historical IDs. Other-name records are excluded; query an ID for its calls across renames. Cannot combine with loop_id. Add group_by=loop_name to combine matching instances into one row.",
				},
				"group_by": map[string]any{
					"type": "string", "enum": []string{"loop", "loop_name", "deployment", "model", "upstream_model", "provider", "resource", "role", "task"},
					"description": "Optional breakdown, ordered by cost descending then key. Defaults to loop with a loop selector, otherwise totals only. provider/role cover all selected records. loop groups by instance ID; loop_name combines exact captured names, so reused names merge and renames split. Blank IDs/names are omitted only from their respective groups, never totals. model aliases deployment.",
				},
				"limit": map[string]any{
					"type": "integer", "minimum": 1, "maximum": 100, "default": 20,
					"description": "Maximum groups (default 20, max 100); the 16 KiB output budget can return fewer. Summary always covers the full selection. Narrow the window or selectors when truncated.",
				},
			},
		},
		Handler: r.handleCostSummary,
	})
}

type costSummaryScope struct {
	Kind     string `json:"kind"`
	LoopID   string `json:"loop_id,omitempty"`
	LoopName string `json:"loop_name,omitempty"`
}

type costSummaryWindow struct {
	Period string `json:"period,omitempty"`
	Since  string `json:"since,omitempty"`
	Until  string `json:"until"`
	Bounds string `json:"bounds"`
}

type costSummaryResult struct {
	Scope          costSummaryScope    `json:"scope"`
	Window         costSummaryWindow   `json:"window"`
	GroupBy        string              `json:"group_by,omitempty"`
	Summary        usage.Summary       `json:"summary"`
	Unattributed   usage.Summary       `json:"unattributed"`
	MatchedGroups  int                 `json:"matched_groups"`
	ReturnedGroups int                 `json:"returned_groups"`
	Groups         []usage.ReportGroup `json:"groups"`
	Truncated      bool                `json:"truncated"`
	Notes          []string            `json:"notes"`
}

func (r *Registry) handleCostSummary(ctx context.Context, args map[string]any) (string, error) {
	now := time.Now()
	opts, period, err := parseCostSummaryOptions(ctx, args, now)
	if err != nil {
		return "", err
	}
	// All-history queries can scan a large ledger; cancellation and a fixed
	// deadline bound the work independently of the output row limit.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	report, err := r.usageStore.Report(ctx, opts)
	if err != nil {
		return "", fmt.Errorf("cost_summary: query recorded usage (try a narrower time window): %w", err)
	}
	result := costSummaryResult{
		Scope: costSummaryScope{Kind: "all_recorded_usage"},
		Window: costSummaryWindow{
			Period: period, Until: promptfmt.FormatDeltaOnly(opts.End, now),
			Bounds: "inclusive_since_exclusive_until",
		},
		GroupBy: opts.GroupBy, Summary: report.Summary, Unattributed: report.Unattributed,
		MatchedGroups: report.MatchedGroups, Groups: report.Groups, Truncated: report.Truncated,
		Notes: []string{
			"Costs are estimates stored at call time, not repriced. Pricing coverage describes recorded usage, not invoice completeness.",
			"Usage records are provider-reported calls, including reported usage on failures; older records may aggregate iterations. Calls without reported usage and embeddings are absent.",
			"For whole-agent spend, omit loop selectors and use provider or role groups; these retain records without loop IDs. Loop groups cover only their captured attribution.",
			"Unattributed totals count matching records without a loop ID; a captured name can still place them in a loop_name group, but cannot recover an instance ID.",
			"Priced records use configured API rates, including explicit zero rates. A $0 estimate does not establish zero resource use; inspect token demand separately.",
		},
	}
	if !opts.Start.IsZero() {
		result.Window.Since = promptfmt.FormatDeltaOnly(opts.Start, now)
	}
	if opts.LoopID != "" {
		result.Scope = costSummaryScope{Kind: "loop_id", LoopID: opts.LoopID}
	}
	if opts.LoopName != "" {
		result.Scope = costSummaryScope{Kind: "recorded_loop_name", LoopName: opts.LoopName}
	}
	if opts.LoopID != "" || opts.LoopName != "" {
		result.Notes = append(result.Notes, "Loop selectors include only directly attributed records, not descendant calls. Name filters cover only records captured under that name.")
	}
	if opts.GroupBy == "loop_name" {
		result.Notes = append(result.Notes, "Name groups combine exact captured names across restarts, including named records without IDs. Reused names merge and renamed records split. Blank names are omitted from groups but remain in matching totals; use group_by=loop to inspect instance IDs.")
	}
	if report.Summary.TotalRecords == 0 {
		result.Notes = append(result.Notes, "No recorded usage matched; this does not establish zero cost. Check the loop ID or name and time window.")
	}
	if report.Summary.UnpricedRecords > 0 {
		result.Notes = append(result.Notes, "Missing prices contribute $0. This is not a complete spend total.")
	}
	if report.Summary.UnknownPricingRecords > 0 {
		result.Notes = append(result.Notes, "Pricing coverage is unknown for some records; stored cost estimates are retained.")
	}
	return marshalCostSummary(result)
}

func marshalCostSummary(result costSummaryResult) (string, error) {
	if result.Groups == nil {
		result.Groups = []usage.ReportGroup{}
	}
	// Drop whole groups so IDs and accounting figures remain exact. Recheck
	// the full JSON after each removal because escaping can expand labels.
	for {
		result.ReturnedGroups = len(result.Groups)
		data, err := json.Marshal(result)
		if err != nil {
			return "", fmt.Errorf("cost_summary: encode recorded usage: %w", err)
		}
		if len(data) <= maxCostSummaryBytes {
			return string(data), nil
		}
		if len(result.Groups) == 0 {
			return "", fmt.Errorf("cost_summary: report metadata exceeds %d bytes; shorten loop selectors", maxCostSummaryBytes)
		}
		result.Groups = result.Groups[:len(result.Groups)-1]
		result.Truncated = true
	}
}
