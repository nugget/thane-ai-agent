package usage

import (
	"fmt"
	"strings"
	"time"
)

// SummaryByLoop returns per-loop aggregated totals for records within
// [start, end), ordered by cost descending. Records without a loop_id
// are excluded from this view.
func (s *Store) SummaryByLoop(start, end time.Time) ([]LoopSummary, error) {
	rows, err := s.db.Query(
		`SELECT loop_id, COALESCE(MAX(loop_name), ''), COUNT(DISTINCT request_id),
		        COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_creation_input_tokens), 0), COALESCE(SUM(cache_read_input_tokens), 0),
		        COALESCE(SUM(cost_usd), 0)
		 FROM usage_records
		 WHERE timestamp >= ? AND timestamp < ? AND loop_id <> ''
		 GROUP BY loop_id
		 ORDER BY SUM(cost_usd) DESC, MAX(timestamp) DESC`,
		start.UTC().Format(time.RFC3339),
		end.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("query usage by loop: %w", err)
	}
	defer rows.Close()

	var result []LoopSummary
	for rows.Next() {
		var ls LoopSummary
		if err := rows.Scan(
			&ls.LoopID,
			&ls.LoopName,
			&ls.RequestCount,
			&ls.Summary.TotalRecords,
			&ls.Summary.TotalInputTokens,
			&ls.Summary.TotalOutputTokens,
			&ls.Summary.TotalCacheCreationInputTokens,
			&ls.Summary.TotalCacheReadInputTokens,
			&ls.Summary.TotalCostUSD,
		); err != nil {
			return nil, fmt.Errorf("scan usage by loop: %w", err)
		}
		result = append(result, ls)
	}
	return result, rows.Err()
}

// TopRequests returns the highest-cost requests within [start, end),
// ordered by total cost descending. Each row aggregates all LLM usage
// records written for that request.
func (s *Store) TopRequests(start, end time.Time, limit int) ([]RequestSummary, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.Query(
		`SELECT request_id,
		        COALESCE(MAX(timestamp), ''),
		        COALESCE(MAX(conversation_id), ''),
		        COALESCE(MAX(session_id), ''),
		        COALESCE(MAX(loop_id), ''),
		        COALESCE(MAX(loop_name), ''),
		        COALESCE(MAX(model), ''),
		        COALESCE(MAX(upstream_model), ''),
		        COALESCE(MAX(resource), ''),
		        COALESCE(MAX(provider), ''),
		        COALESCE(MAX(role), ''),
		        COALESCE(MAX(task_name), ''),
		        COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_creation_input_tokens), 0), COALESCE(SUM(cache_read_input_tokens), 0),
		        COALESCE(SUM(cost_usd), 0)
		 FROM usage_records
		 WHERE timestamp >= ? AND timestamp < ?
		 GROUP BY request_id
		 ORDER BY SUM(cost_usd) DESC, MAX(timestamp) DESC
		 LIMIT ?`,
		start.UTC().Format(time.RFC3339),
		end.UTC().Format(time.RFC3339),
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query top requests: %w", err)
	}
	defer rows.Close()

	var result []RequestSummary
	for rows.Next() {
		var rs RequestSummary
		if err := rows.Scan(
			&rs.RequestID,
			&rs.CreatedAt,
			&rs.ConversationID,
			&rs.SessionID,
			&rs.LoopID,
			&rs.LoopName,
			&rs.Model,
			&rs.UpstreamModel,
			&rs.Resource,
			&rs.Provider,
			&rs.Role,
			&rs.TaskName,
			&rs.Summary.TotalRecords,
			&rs.Summary.TotalInputTokens,
			&rs.Summary.TotalOutputTokens,
			&rs.Summary.TotalCacheCreationInputTokens,
			&rs.Summary.TotalCacheReadInputTokens,
			&rs.Summary.TotalCostUSD,
		); err != nil {
			return nil, fmt.Errorf("scan top requests: %w", err)
		}
		result = append(result, rs)
	}
	return result, rows.Err()
}

// SummaryByGroup dispatches the grouped summary query based on the
// caller-provided grouping key.
func (s *Store) SummaryByGroup(groupBy string, start, end time.Time) ([]GroupedSummary, error) {
	switch strings.TrimSpace(groupBy) {
	case "deployment", "model":
		return s.SummaryByModel(start, end)
	case "upstream_model":
		return s.SummaryByUpstreamModel(start, end)
	case "provider":
		return s.SummaryByProvider(start, end)
	case "resource":
		return s.SummaryByResource(start, end)
	case "role":
		return s.SummaryByRole(start, end)
	case "task":
		return s.SummaryByTask(start, end)
	default:
		return nil, fmt.Errorf("unsupported group_by %q; use one of [\"deployment\" \"model\" \"upstream_model\" \"provider\" \"resource\" \"role\" \"task\"]", groupBy)
	}
}

func (s *Store) summaryGroupedBy(column string, start, end time.Time) ([]GroupedSummary, error) {
	// column is always a compile-time constant from our own methods,
	// never user input, so embedding it directly is safe.
	query := fmt.Sprintf(
		`SELECT COALESCE(%s, ''), COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_creation_input_tokens), 0), COALESCE(SUM(cache_read_input_tokens), 0), COALESCE(SUM(cost_usd), 0)
		 FROM usage_records
		 WHERE timestamp >= ? AND timestamp < ?
		 GROUP BY %s
		 ORDER BY SUM(cost_usd) DESC`,
		column, column,
	)

	rows, err := s.db.Query(query,
		start.UTC().Format(time.RFC3339),
		end.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("query usage by %s: %w", column, err)
	}
	defer rows.Close()

	var result []GroupedSummary
	for rows.Next() {
		var gs GroupedSummary
		if err := rows.Scan(&gs.Key, &gs.Summary.TotalRecords, &gs.Summary.TotalInputTokens, &gs.Summary.TotalOutputTokens, &gs.Summary.TotalCacheCreationInputTokens, &gs.Summary.TotalCacheReadInputTokens, &gs.Summary.TotalCostUSD); err != nil {
			return nil, fmt.Errorf("scan usage by %s: %w", column, err)
		}
		result = append(result, gs)
	}
	return result, rows.Err()
}
