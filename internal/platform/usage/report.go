package usage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ReportOptions selects recorded usage within [Start, End). Start may be zero
// for all retained history; End must be nonzero and later than Start. LoopID
// and LoopName are mutually exclusive exact filters. LoopID selects only that
// loop's own records, never descendants. LoopName selects the label captured
// on each record; records captured under another name are outside that selection.
//
// GroupBy accepts deployment (or model), upstream_model, provider, resource,
// role, task, loop, or loop_name. Loop groups by instance ID; loop_name combines
// exact captured names across instance IDs, including records with no ID. Names
// reused by unrelated loops combine, while a renamed loop splits across names.
// Empty returns totals only. Limit bounds returned groups, not the totals: zero
// defaults to 20, and explicit values must be 1 through 100.
type ReportOptions struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	LoopID   string    `json:"loop_id,omitempty"`
	LoopName string    `json:"loop_name,omitempty"`
	GroupBy  string    `json:"group_by,omitempty"`
	Limit    int       `json:"limit,omitempty"`
}

// Report contains complete totals for the selected records and a bounded list
// of groups from the same read snapshot. Unattributed is the subset with no
// captured loop ID; it may include historical or non-loop work and is never
// assigned to a known loop. For loop grouping it is excluded from Groups and
// MatchedGroups. Loop-name grouping instead excludes records with blank captured
// names from Groups and MatchedGroups, retaining them in Summary; named records
// without a loop ID remain both grouped and Unattributed. Other groupings include
// blank keys. MatchedGroups counts all matching groups before Limit, and Truncated
// reports omitted groups. Both are zero when grouping is disabled. Costs retain
// the values recorded at call time.
type Report struct {
	Summary       Summary       `json:"summary"`
	Unattributed  Summary       `json:"unattributed"`
	MatchedGroups int           `json:"matched_groups"`
	Groups        []ReportGroup `json:"groups"`
	Truncated     bool          `json:"truncated"`
}

// ReportGroup associates a grouping key with all its selected usage. LoopName
// is populated only for loop grouping and is the latest captured label within
// the selection, with record ID breaking timestamp ties. It is not a lookup of
// the loop's current name or evidence that the loop still exists.
type ReportGroup struct {
	Key      string  `json:"key"`
	LoopName string  `json:"loop_name,omitempty"`
	Summary  Summary `json:"summary"`
}

const reportTotalsSQL = `COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
	COALESCE(SUM(cache_creation_input_tokens), 0), COALESCE(SUM(cache_read_input_tokens), 0),
	COALESCE(SUM(cost_usd), 0), COUNT(CASE WHEN pricing_status = 'priced' THEN 1 END),
	COUNT(CASE WHEN pricing_status = 'unpriced' THEN 1 END),
	COUNT(CASE WHEN pricing_status NOT IN ('priced', 'unpriced') THEN 1 END)`

// Report reads recorded costs, tokens, and pricing coverage with caller
// cancellation. All filters are applied before grouping and limiting. Groups
// sort by recorded cost descending, then key ascending. Invalid options fail
// before database access; query, scan, or cancellation failures return no partial
// report. Empty selections succeed with zero totals and an empty Groups slice.
func (s *Store) Report(ctx context.Context, opts ReportOptions) (*Report, error) {
	column, err := validateReportOptions(&opts)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin usage report: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	report, err := readReport(ctx, tx, opts, column)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("finish usage report: %w", err)
	}
	return report, nil
}

// Keeping reads separate from transaction ownership lets multi-window views
// reuse the same filters and totals without observing different ledger states.
func readReport(ctx context.Context, tx *sql.Tx, opts ReportOptions, column string) (*Report, error) {
	report := &Report{Groups: []ReportGroup{}}
	where, args := reportWhere("u", opts)
	if err := tx.QueryRowContext(ctx, `SELECT `+reportTotalsSQL+` FROM usage_records u WHERE `+where, args...).Scan(reportSummaryArgs(&report.Summary)...); err != nil {
		return nil, fmt.Errorf("query usage report totals: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT `+reportTotalsSQL+` FROM usage_records u WHERE `+where+` AND u.loop_id = ''`, args...).Scan(reportSummaryArgs(&report.Unattributed)...); err != nil {
		return nil, fmt.Errorf("query unattributed usage: %w", err)
	}
	if column != "" {
		if err := readReportGroups(ctx, tx, opts, column, where, args, report); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return report, nil
}

func validateReportOptions(opts *ReportOptions) (string, error) {
	if opts.End.IsZero() || !opts.Start.Before(opts.End) {
		return "", fmt.Errorf("usage report requires start before a nonzero end")
	}
	if opts.LoopID != "" && opts.LoopName != "" {
		return "", fmt.Errorf("loop_id and loop_name are mutually exclusive")
	}
	if opts.Limit == 0 {
		opts.Limit = 20
	}
	if opts.Limit < 1 || opts.Limit > 100 {
		return "", fmt.Errorf("limit must be between 1 and 100")
	}
	switch opts.GroupBy {
	case "", "upstream_model", "provider", "resource", "role", "loop_name":
		return opts.GroupBy, nil
	case "deployment", "model":
		return "model", nil
	case "task":
		return "task_name", nil
	case "loop":
		return "loop_id", nil
	default:
		return "", fmt.Errorf("unsupported group_by %q; use deployment, model, upstream_model, provider, resource, role, task, loop, or loop_name", opts.GroupBy)
	}
}

func reportWhere(alias string, opts ReportOptions) (string, []any) {
	clauses := []string{alias + ".timestamp >= ?", alias + ".timestamp < ?"}
	args := []any{reportTimeBound(opts.Start), reportTimeBound(opts.End)}
	if opts.LoopID != "" {
		clauses = append(clauses, alias+".loop_id = ?")
		args = append(args, opts.LoopID)
	}
	if opts.LoopName != "" {
		clauses = append(clauses, alias+".loop_name = ?")
		args = append(args, opts.LoopName)
	}
	return strings.Join(clauses, " AND "), args
}

// Record timestamps are canonical UTC whole seconds. Rounding both bounds up
// preserves [start, end) comparisons against those instants without applying a
// function to the indexed timestamp column or truncating a fractional bound.
func reportTimeBound(t time.Time) string {
	if t.Nanosecond() != 0 {
		t = t.Truncate(time.Second).Add(time.Second)
	}
	return t.UTC().Format(time.RFC3339)
}

func reportSummaryArgs(sum *Summary) []any {
	return []any{&sum.TotalRecords, &sum.TotalInputTokens, &sum.TotalOutputTokens,
		&sum.TotalCacheCreationInputTokens, &sum.TotalCacheReadInputTokens, &sum.TotalCostUSD,
		&sum.PricedRecords, &sum.UnpricedRecords, &sum.UnknownPricingRecords}
}

func readReportGroups(ctx context.Context, tx *sql.Tx, opts ReportOptions, column, where string, args []any, report *Report) error {
	// column and aliases originate only from this package's whitelist.
	key := "COALESCE(u." + column + ", '')"
	loopName := "''"
	var selectArgs []any
	switch column {
	case "loop_id":
		where += " AND u.loop_id <> ''"
		latestWhere, latestArgs := reportWhere("latest", opts)
		loopName = `(SELECT latest.loop_name FROM usage_records latest
			WHERE latest.loop_id = u.loop_id AND ` + latestWhere + `
			ORDER BY latest.timestamp DESC, latest.id DESC LIMIT 1)`
		selectArgs = latestArgs
	case "loop_name":
		where += " AND u.loop_name <> ''"
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT `+key+`
		FROM usage_records u WHERE `+where+` GROUP BY `+key+`)`, args...).Scan(&report.MatchedGroups); err != nil {
		return fmt.Errorf("count usage report groups: %w", err)
	}
	selectArgs = append(selectArgs, args...)
	selectArgs = append(selectArgs, opts.Limit)
	rows, err := tx.QueryContext(ctx, `SELECT `+key+`, `+loopName+`, `+reportTotalsSQL+`
		FROM usage_records u WHERE `+where+` GROUP BY `+key+`
		ORDER BY SUM(cost_usd) DESC, `+key+` ASC LIMIT ?`, selectArgs...)
	if err != nil {
		return fmt.Errorf("query usage report groups: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var group ReportGroup
		if err := rows.Scan(append([]any{&group.Key, &group.LoopName}, reportSummaryArgs(&group.Summary)...)...); err != nil {
			return fmt.Errorf("scan usage report group: %w", err)
		}
		report.Groups = append(report.Groups, group)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read usage report groups: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close usage report groups: %w", err)
	}
	report.Truncated = report.MatchedGroups > len(report.Groups)
	return nil
}
