package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Values returns observed frontmatter values for one key.
func (s *Store) Values(ctx context.Context, root, key string, limit int) ([]ValueCount, error) {
	return s.values(ctx, root, key, limit, true)
}

func (s *Store) values(ctx context.Context, root, key string, limit int, refresh bool) ([]ValueCount, error) {
	if refresh {
		if err := s.Refresh(ctx); err != nil {
			return nil, err
		}
	}
	root = strings.TrimSuffix(strings.TrimSpace(root), ":")
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return nil, fmt.Errorf("key is required")
	}
	if root != "" && !rootExists(s.roots, root) {
		return nil, fmt.Errorf("unknown document root %q", root)
	}

	var (
		rows *sql.Rows
		err  error
	)
	if root == "" {
		rows, err = s.db.QueryContext(ctx, `SELECT tags_json, frontmatter_json FROM indexed_documents`)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT tags_json, frontmatter_json FROM indexed_documents WHERE root = ?`, root)
	}
	if err != nil {
		return nil, fmt.Errorf("query document values: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var tagsJSON, metaJSON string
		if err := rows.Scan(&tagsJSON, &metaJSON); err != nil {
			return nil, fmt.Errorf("scan document values: %w", err)
		}
		if key == "tags" {
			var tags []string
			if jsonErr := json.Unmarshal([]byte(tagsJSON), &tags); jsonErr == nil {
				for _, tag := range tags {
					counts[tag]++
				}
			}
			continue
		}
		var meta map[string][]string
		if jsonErr := json.Unmarshal([]byte(metaJSON), &meta); jsonErr == nil {
			for _, value := range meta[key] {
				counts[value]++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	values := asValueCounts(counts)
	limit = clampLimit(limit, 20, 100)
	if len(values) > limit {
		values = values[:limit]
	}
	return values, nil
}
