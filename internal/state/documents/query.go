package documents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// Roots returns the current indexed root summaries.
func (s *Store) Roots(ctx context.Context) ([]RootSummary, error) {
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}
	summaries := make([]RootSummary, 0, len(s.roots))
	for _, root := range s.allRoots() {
		summary := RootSummary{
			Root:         root,
			Path:         s.roots[root],
			Policy:       s.rootPolicySummary(root),
			Verification: s.rootVerificationSummary(ctx, root),
		}
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*), COALESCE(SUM(size_bytes), 0), COALESCE(SUM(word_count), 0), COALESCE(MAX(modified_at), '')
			 FROM indexed_documents
			 WHERE root = ?`,
			root,
		).Scan(&summary.DocumentCount, &summary.TotalSizeBytes, &summary.TotalWordCount, &summary.LastModifiedAt); err != nil {
			return nil, fmt.Errorf("summarize documents for root %q: %w", root, err)
		}
		topTags, err := s.values(ctx, root, "tags", 8, false)
		if err != nil {
			return nil, err
		}
		tagValues := make([]string, 0, len(topTags))
		for _, tag := range topTags {
			tagValues = append(tagValues, tag.Value)
		}
		summary.TopTags = tagValues
		topDirectories, err := s.topDirectories(ctx, root, 5)
		if err != nil {
			return nil, err
		}
		summary.TopDirectories = topDirectories
		recentDocuments, err := s.recentDocuments(ctx, root, 3)
		if err != nil {
			return nil, err
		}
		summary.RecentDocuments = recentDocuments
		summaries = append(summaries, summary)
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
	args := []any{root}
	query := `SELECT root, rel_path, title, summary, tags_json, frontmatter_json, modified_at, word_count
		 FROM indexed_documents
		 WHERE root = ?`
	if prefix != "" {
		query += ` AND (rel_path = ? OR rel_path LIKE ?)`
		args = append(args, prefix, prefix+"/%")
	}
	query += ` ORDER BY rel_path`
	rows, err := s.db.QueryContext(ctx,
		query,
		args...,
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
	q.Frontmatter = normalizeSearchFrontmatter(q.Frontmatter)
	q.FrontmatterKeys = dedupeSorted(q.FrontmatterKeys)
	if q.ModifiedAfter != nil && q.ModifiedBefore != nil && q.ModifiedAfter.After(*q.ModifiedBefore) {
		return nil, fmt.Errorf("modified_after must be earlier than modified_before")
	}

	var args []any
	var where []string
	query := `SELECT root, rel_path, title, summary, tags_json, frontmatter_json, modified_at, word_count FROM indexed_documents`
	if q.Root != "" {
		if !rootExists(s.roots, q.Root) {
			return nil, fmt.Errorf("unknown document root %q", q.Root)
		}
		where = append(where, "root = ?")
		args = append(args, q.Root)
	}
	if q.PathPrefix != "" {
		where = append(where, `(rel_path = ? OR rel_path LIKE ?)`)
		args = append(args, q.PathPrefix, q.PathPrefix+"/%")
	}
	if q.Query != "" {
		like := "%" + q.Query + "%"
		where = append(where, `(LOWER(title) LIKE ? OR LOWER(summary) LIKE ? OR LOWER(rel_path) LIKE ? OR LOWER(tags_json) LIKE ?)`)
		args = append(args, like, like, like, like)
	}
	if q.ModifiedAfter != nil {
		where = append(where, `modified_at >= ?`)
		args = append(args, q.ModifiedAfter.UTC().Format(time.RFC3339Nano))
	}
	if q.ModifiedBefore != nil {
		where = append(where, `modified_at <= ?`)
		args = append(args, q.ModifiedBefore.UTC().Format(time.RFC3339Nano))
	}
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
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
		if !hasAllTags(doc.Tags, q.Tags) {
			continue
		}
		if !hasFrontmatterKeys(doc.Frontmatter, q.FrontmatterKeys) {
			continue
		}
		if !matchesFrontmatter(doc.Frontmatter, q.Frontmatter) {
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
			ti, _ := database.ParseTimestamp(matches[i].doc.ModifiedAt)
			tj, _ := database.ParseTimestamp(matches[j].doc.ModifiedAt)
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
	if err := s.verifyDocumentForConsumer(ctx, root, relPath, "doc_outline"); err != nil {
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
	if err := s.verifyDocumentForConsumer(ctx, root, relPath, "doc_section"); err != nil {
		return nil, err
	}
	if exists, err := s.documentExists(ctx, root, relPath); err != nil {
		return nil, err
	} else if !exists {
		return nil, fmt.Errorf("document not found: %s", ref)
	}
	absPath, err := s.resolveDocumentPath(root, relPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("document not found: %s", ref)
		}
		return nil, fmt.Errorf("resolve document: %w", err)
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("document not found: %s", ref)
		}
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
