package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/usage"
)

// registerCostSummary registers the cost_summary tool for querying
// token usage and API costs.
func (r *Registry) registerCostSummary() {
	if r.usageStore == nil {
		return
	}

	r.Register(&Tool{
		Name:        "cost_summary",
		Description: "Query your own token usage and API costs. Returns totals and optional breakdown by model, role, or task. Use to understand spending patterns and resource consumption.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"period": map[string]any{
					"type":        "string",
					"enum":        []string{"today", "yesterday", "week", "month", "all"},
					"description": "Time period to summarize.",
				},
				"group_by": map[string]any{
					"type":        "string",
					"enum":        []string{"model", "role", "task"},
					"description": "Optional: group results by model, role, or task name.",
				},
			},
			"required": []string{"period"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			period, _ := args["period"].(string)
			groupBy, _ := args["group_by"].(string)

			start, end := parsePeriod(period)

			summary, err := r.usageStore.Summary(start, end)
			if err != nil {
				return "", fmt.Errorf("query usage summary: %w", err)
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Cost Summary (%s):\n", period))
			sb.WriteString(fmt.Sprintf("  Total requests: %d\n", summary.TotalRecords))
			sb.WriteString(fmt.Sprintf("  Input tokens: %s\n", formatTokenCount(summary.TotalInputTokens)))
			sb.WriteString(fmt.Sprintf("  Output tokens: %s\n", formatTokenCount(summary.TotalOutputTokens)))
			sb.WriteString(fmt.Sprintf("  Estimated cost: $%.4f\n", summary.TotalCostUSD))

			if groupBy != "" {
				grouped, groupLabel, err := queryGrouped(r.usageStore, groupBy, start, end)
				if err != nil {
					return "", err
				}
				if len(grouped) > 0 {
					sb.WriteString(fmt.Sprintf("\nBy %s:\n", groupLabel))
					for key, sum := range grouped {
						display := key
						if display == "" {
							display = "(none)"
						}
						sb.WriteString(fmt.Sprintf("  %s: $%.4f (%d requests, %s in / %s out)\n",
							display, sum.TotalCostUSD, sum.TotalRecords,
							formatTokenCount(sum.TotalInputTokens),
							formatTokenCount(sum.TotalOutputTokens),
						))
					}
				}
			}

			return sb.String(), nil
		},
	})
}

// queryGrouped dispatches the grouped summary query based on the
// group_by parameter.
func queryGrouped(store *usage.Store, groupBy string, start, end time.Time) (map[string]*usage.Summary, string, error) {
	switch groupBy {
	case "model":
		result, err := store.SummaryByModel(start, end)
		return result, "Model", err
	case "role":
		result, err := store.SummaryByRole(start, end)
		return result, "Role", err
	case "task":
		result, err := store.SummaryByTask(start, end)
		return result, "Task", err
	default:
		return nil, "", nil
	}
}

// parsePeriod converts a period name to a start/end time range.
func parsePeriod(period string) (time.Time, time.Time) {
	now := time.Now()
	end := now.Add(1 * time.Minute) // slight future buffer

	switch period {
	case "today":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return start, end
	case "yesterday":
		yesterday := now.AddDate(0, 0, -1)
		start := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, yesterday.Location())
		endOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return start, endOfDay
	case "week":
		return now.AddDate(0, 0, -7), end
	case "month":
		return now.AddDate(0, -1, 0), end
	case "all":
		return time.Time{}, end
	default:
		return time.Time{}, end
	}
}

// formatTokenCount formats a token count as a compact string (e.g.,
// "1.23M", "456.0K", "789").
func formatTokenCount(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000.0)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000.0)
	}
	return fmt.Sprintf("%d", n)
}
