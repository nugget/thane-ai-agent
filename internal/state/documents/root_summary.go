package documents

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func (s *Store) topDirectories(ctx context.Context, root string, limit int) ([]BrowseDirectory, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT rel_path FROM indexed_documents WHERE root = ? ORDER BY rel_path`,
		root,
	)
	if err != nil {
		return nil, fmt.Errorf("query top directories for root %q: %w", root, err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var relPath string
		if err := rows.Scan(&relPath); err != nil {
			return nil, fmt.Errorf("scan top directory candidate for root %q: %w", root, err)
		}
		trimmed := trimPathPrefix(relPath)
		if trimmed == "" {
			continue
		}
		slash := strings.Index(trimmed, "/")
		if slash < 0 {
			continue
		}
		counts[trimmed[:slash]]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	directories := make([]BrowseDirectory, 0, len(counts))
	for name, count := range counts {
		directories = append(directories, BrowseDirectory{
			Name:          name,
			PathPrefix:    name,
			DocumentCount: count,
		})
	}
	sort.Slice(directories, func(i, j int) bool {
		if directories[i].DocumentCount == directories[j].DocumentCount {
			return directories[i].PathPrefix < directories[j].PathPrefix
		}
		return directories[i].DocumentCount > directories[j].DocumentCount
	})
	if limit > 0 && len(directories) > limit {
		directories = directories[:limit]
	}
	return directories, nil
}

func (s *Store) recentDocuments(ctx context.Context, root string, limit int) ([]RootDocumentHint, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT rel_path, title, modified_at
		 FROM indexed_documents
		 WHERE root = ?
		 ORDER BY modified_at DESC, rel_path
		 LIMIT ?`,
		root, clampLimit(limit, 3, 10),
	)
	if err != nil {
		return nil, fmt.Errorf("query recent documents for root %q: %w", root, err)
	}
	defer rows.Close()

	var docs []RootDocumentHint
	for rows.Next() {
		var doc RootDocumentHint
		if err := rows.Scan(&doc.Path, &doc.Title, &doc.ModifiedAt); err != nil {
			return nil, fmt.Errorf("scan recent document for root %q: %w", root, err)
		}
		doc.Ref = makeRef(root, doc.Path)
		docs = append(docs, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return docs, nil
}
