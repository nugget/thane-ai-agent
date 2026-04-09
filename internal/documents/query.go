package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Roots returns the current indexed root summaries.
func (s *Store) Roots(ctx context.Context) ([]RootSummary, error) {
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}
	summaries := make([]RootSummary, 0, len(s.roots))
	for _, root := range s.indexedRoots() {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM indexed_documents WHERE root = ?`, root).Scan(&count); err != nil {
			return nil, fmt.Errorf("count documents for root %q: %w", root, err)
		}
		topTags, err := s.values(ctx, root, "tags", 8, false)
		if err != nil {
			return nil, err
		}
		tagValues := make([]string, 0, len(topTags))
		for _, tag := range topTags {
			tagValues = append(tagValues, tag.Value)
		}
		summaries = append(summaries, RootSummary{
			Root:          root,
			Path:          s.roots[root],
			DocumentCount: count,
			TopTags:       tagValues,
		})
	}
	return summaries, nil
}

// Browse returns the immediate child directories and documents for a rooted prefix.
func (s *Store) Browse(ctx context.Context, root, prefix string, limit int) (*BrowseResult, error) {
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}
	root = strings.TrimSuffix(strings.TrimSpace(root), ":")
	if !rootExists(s.roots, root) {
		return nil, fmt.Errorf("unknown document root %q", root)
	}
	prefix = trimPathPrefix(prefix)
	rows, err := s.db.QueryContext(ctx,
		`SELECT root, rel_path, title, summary, tags_json, frontmatter_json, modified_at, word_count
		 FROM indexed_documents
		 WHERE root = ?
		 ORDER BY rel_path`,
		root,
	)
	if err != nil {
		return nil, fmt.Errorf("browse documents: %w", err)
	}
	defer rows.Close()

	dirCounts := make(map[string]int)
	var docs []DocumentSummary
	for rows.Next() {
		var doc DocumentSummary
		if err := scanDocument(rows, &doc); err != nil {
			return nil, fmt.Errorf("scan browse document: %w", err)
		}
		rel := doc.Path
		if prefix != "" {
			if rel != prefix && !strings.HasPrefix(rel, prefix+"/") {
				continue
			}
			rel = strings.TrimPrefix(strings.TrimPrefix(rel, prefix), "/")
		}
		if rel == "" {
			docs = append(docs, doc)
			continue
		}
		if cut := strings.Index(rel, "/"); cut >= 0 {
			child := rel[:cut]
			nextPrefix := child
			if prefix != "" {
				nextPrefix = prefix + "/" + child
			}
			dirCounts[nextPrefix]++
			continue
		}
		docs = append(docs, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	directories := make([]BrowseDirectory, 0, len(dirCounts))
	for nextPrefix, count := range dirCounts {
		name := nextPrefix
		if idx := strings.LastIndex(nextPrefix, "/"); idx >= 0 {
			name = nextPrefix[idx+1:]
		}
		directories = append(directories, BrowseDirectory{
			Name:          name,
			PathPrefix:    nextPrefix,
			DocumentCount: count,
		})
	}
	sort.Slice(directories, func(i, j int) bool { return directories[i].PathPrefix < directories[j].PathPrefix })
	sort.Slice(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })

	limit = clampLimit(limit, 20, 100)
	directories, docs = limitBrowseResults(directories, docs, limit)

	return &BrowseResult{
		Root:        root,
		PathPrefix:  prefix,
		Directories: directories,
		Documents:   docs,
	}, nil
}

// Search returns matching documents, sorted by relevance and recency.
func (s *Store) Search(ctx context.Context, q SearchQuery) ([]DocumentSummary, error) {
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}
	q.Limit = clampLimit(q.Limit, 20, 100)
	q.Root = strings.TrimSuffix(strings.TrimSpace(q.Root), ":")
	q.PathPrefix = trimPathPrefix(q.PathPrefix)
	q.Query = strings.TrimSpace(strings.ToLower(q.Query))
	q.Tags = dedupeSorted(q.Tags)

	var args []any
	query := `SELECT root, rel_path, title, summary, tags_json, frontmatter_json, modified_at, word_count FROM indexed_documents`
	if q.Root != "" {
		if !rootExists(s.roots, q.Root) {
			return nil, fmt.Errorf("unknown document root %q", q.Root)
		}
		query += ` WHERE root = ?`
		args = append(args, q.Root)
	}
	query += ` ORDER BY modified_at DESC, rel_path`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search documents: %w", err)
	}
	defer rows.Close()

	type scored struct {
		doc   DocumentSummary
		score int
	}
	var matches []scored
	for rows.Next() {
		var doc DocumentSummary
		if err := scanDocument(rows, &doc); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		if q.PathPrefix != "" && doc.Path != q.PathPrefix && !strings.HasPrefix(doc.Path, q.PathPrefix+"/") {
			continue
		}
		if !hasAllTags(doc.Tags, q.Tags) {
			continue
		}
		score := matchScore(doc, q.Query)
		if q.Query != "" && score == 0 {
			continue
		}
		matches = append(matches, scored{doc: doc, score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			ti, _ := time.Parse(time.RFC3339Nano, matches[i].doc.ModifiedAt)
			tj, _ := time.Parse(time.RFC3339Nano, matches[j].doc.ModifiedAt)
			if ti.Equal(tj) {
				return matches[i].doc.Ref < matches[j].doc.Ref
			}
			return ti.After(tj)
		}
		return matches[i].score > matches[j].score
	})

	if len(matches) > q.Limit {
		matches = matches[:q.Limit]
	}
	out := make([]DocumentSummary, 0, len(matches))
	for _, match := range matches {
		out = append(out, match.doc)
	}
	return out, nil
}

// Outline returns the heading tree for a document ref.
func (s *Store) Outline(ctx context.Context, ref string) ([]Section, error) {
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}
	root, relPath, err := parseRef(ref)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT level, heading, slug, start_line, end_line
		 FROM indexed_document_sections
		 WHERE root = ? AND rel_path = ?
		 ORDER BY ordinal`,
		root, relPath,
	)
	if err != nil {
		return nil, fmt.Errorf("query outline: %w", err)
	}
	defer rows.Close()

	var sections []Section
	for rows.Next() {
		var sec Section
		if err := rows.Scan(&sec.Level, &sec.Heading, &sec.Slug, &sec.StartLine, &sec.EndLine); err != nil {
			return nil, fmt.Errorf("scan outline section: %w", err)
		}
		sections = append(sections, sec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(sections) == 0 {
		if exists, err := s.documentExists(ctx, root, relPath); err != nil {
			return nil, err
		} else if !exists {
			return nil, fmt.Errorf("document not found: %s", ref)
		}
	}
	return sections, nil
}

// Section returns one section by heading text or slug. An empty selector returns the whole document.
func (s *Store) Section(ctx context.Context, ref string, selector string) (*Section, error) {
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}
	root, relPath, err := parseRef(ref)
	if err != nil {
		return nil, err
	}
	if exists, err := s.documentExists(ctx, root, relPath); err != nil {
		return nil, err
	} else if !exists {
		return nil, fmt.Errorf("document not found: %s", ref)
	}
	absPath, err := s.resolveDocumentPath(root, relPath)
	if err != nil {
		return nil, fmt.Errorf("resolve document: %w", err)
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read document: %w", err)
	}
	meta, body := splitFrontmatter(string(raw))
	doc := parseMarkdownDocumentParts(relPath, meta, body)
	selector = strings.TrimSpace(selector)
	if selector == "" {
		lineCount := len(strings.Split(body, "\n"))
		return &Section{
			Heading:   doc.Title,
			Slug:      slugify(doc.Title),
			Level:     0,
			StartLine: 1,
			EndLine:   lineCount,
			Content:   strings.TrimSpace(body),
		}, nil
	}
	targetSlug := slugify(selector)
	for _, sec := range doc.Sections {
		if strings.EqualFold(sec.Heading, selector) || sec.Slug == targetSlug {
			return &sec, nil
		}
	}
	return nil, fmt.Errorf("section %q not found in %s", selector, ref)
}

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

func (s *Store) documentExists(ctx context.Context, root, relPath string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM indexed_documents WHERE root = ? AND rel_path = ?`,
		root, relPath,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("check document existence: %w", err)
	}
	return count > 0, nil
}

func hasAllTags(docTags, required []string) bool {
	if len(required) == 0 {
		return true
	}
	set := make(map[string]bool, len(docTags))
	for _, tag := range docTags {
		set[strings.ToLower(strings.TrimSpace(tag))] = true
	}
	for _, tag := range required {
		if !set[strings.ToLower(strings.TrimSpace(tag))] {
			return false
		}
	}
	return true
}

func matchScore(doc DocumentSummary, query string) int {
	if query == "" {
		return 1
	}
	q := strings.ToLower(query)
	score := 0
	if strings.Contains(strings.ToLower(doc.Title), q) {
		score += 10
	}
	if strings.Contains(strings.ToLower(doc.Path), q) {
		score += 6
	}
	if strings.Contains(strings.ToLower(doc.Summary), q) {
		score += 4
	}
	for _, tag := range doc.Tags {
		if strings.Contains(strings.ToLower(tag), q) {
			score += 3
		}
	}
	return score
}

func clampLimit(limit, def, max int) int {
	if limit <= 0 {
		return def
	}
	if limit > max {
		return max
	}
	return limit
}

func limitBrowseResults(directories []BrowseDirectory, docs []DocumentSummary, limit int) ([]BrowseDirectory, []DocumentSummary) {
	limitedDirectories := make([]BrowseDirectory, 0, min(limit, len(directories)))
	limitedDocs := make([]DocumentSummary, 0, min(limit, len(docs)))
	for dirIdx, docIdx := 0, 0; len(limitedDirectories)+len(limitedDocs) < limit && (dirIdx < len(directories) || docIdx < len(docs)); {
		switch {
		case dirIdx >= len(directories):
			limitedDocs = append(limitedDocs, docs[docIdx])
			docIdx++
		case docIdx >= len(docs):
			limitedDirectories = append(limitedDirectories, directories[dirIdx])
			dirIdx++
		case directories[dirIdx].PathPrefix <= docs[docIdx].Path:
			limitedDirectories = append(limitedDirectories, directories[dirIdx])
			dirIdx++
		default:
			limitedDocs = append(limitedDocs, docs[docIdx])
			docIdx++
		}
	}
	return limitedDirectories, limitedDocs
}
