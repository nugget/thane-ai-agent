package documents

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// ContentSearchHit preserves a document's authored summary separately from a
// bounded excerpt around matching text. MatchType is phrase or terms; WriteTool
// identifies the same authoritative writer advertised by document discovery.
type ContentSearchHit struct {
	Document  DocumentSummary `json:"document"`
	Excerpt   string          `json:"excerpt"`
	MatchType string          `json:"match_type"`
	WriteTool string          `json:"write_tool"`
}

// ContentSearchRoot describes search coverage without exposing filesystem paths.
// Status is searched, on_request (excluded unless named), or disabled. Body is
// true only when logical body indexing is enabled; otherwise only metadata is
// searchable. A searched root can contain no indexed or matching documents.
type ContentSearchRoot struct {
	Root   string `json:"root"`
	Body   bool   `json:"body"`
	Status string `json:"status"`
}

// ContentSearchResult contains relevance-ordered matches and root coverage.
// Truncated reports that additional eligible matches exceeded the result limit.
type ContentSearchResult struct {
	Hits      []ContentSearchHit  `json:"hits"`
	Roots     []ContentSearchRoot `json:"roots"`
	Truncated bool                `json:"truncated"`
}

// SearchContent searches indexed metadata and explicitly opted-in logical full
// bodies. It first searches a literal phrase, falling back to all query terms
// only when the phrase has no matches. Query syntax is never interpreted as SQL
// or raw FTS operators. Root visibility, restricted-audience selection, and the
// structured filters have the same meaning as [Store.Search]. Signature policy
// is checked again before returning indexed excerpts.
//
// The content index is derived SQLite state, not a source archive. Refresh
// backfills old metadata rows, updates changed files, removes deleted files,
// and removes body text when a root opts out. Refresh follows the store's normal
// interval; exact reads remain the authority for current source bytes.
func (s *Store) SearchContent(ctx context.Context, q SearchQuery) (*ContentSearchResult, error) {
	q.Root = normalizeRootName(q.Root)
	q.Query = strings.TrimSpace(q.Query)
	q.PathPrefix = trimPathPrefix(q.PathPrefix)
	q.Frontmatter = normalizeSearchFrontmatter(q.Frontmatter)
	q.FrontmatterKeys = dedupeSorted(q.FrontmatterKeys)
	q.Tags = dedupeSorted(q.Tags)
	q.Limit = clampLimit(q.Limit, 20, 100)
	if q.Root != "" && !rootExists(s.roots, q.Root) {
		return nil, fmt.Errorf("unknown document root %q", q.Root)
	}
	if q.ModifiedAfter != nil && q.ModifiedBefore != nil && q.ModifiedAfter.After(*q.ModifiedBefore) {
		return nil, fmt.Errorf("modified_after must be earlier than modified_before")
	}
	if q.Query == "" || !strings.ContainsFunc(q.Query, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) {
		return nil, fmt.Errorf("query must contain searchable words or numbers")
	}
	if len([]rune(q.Query)) > 1024 {
		return nil, fmt.Errorf("query exceeds 1024 characters")
	}
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}
	result := &ContentSearchResult{Hits: []ContentSearchHit{}}
	var roots []string
	for _, root := range s.allRoots() {
		if q.Root != "" && root != q.Root {
			continue
		}
		policy := s.rootPolicy(root)
		coverage := ContentSearchRoot{Root: root, Body: s.indexBody(root), Status: "searched"}
		switch {
		case !policy.Indexing || policy.Context.EffectiveSearch() == RootSearchNever:
			coverage.Status = "disabled"
		case policy.Context.EffectiveSearch() == RootSearchOnRequest && root != q.Root:
			coverage.Status = "on_request"
		default:
			roots = append(roots, root)
		}
		result.Roots = append(result.Roots, coverage)
	}
	if len(roots) == 0 {
		return result, nil
	}
	hits, err := s.searchContentMatches(ctx, q, roots, quoteContentQuery(q.Query), "phrase")
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		terms := strings.Fields(q.Query)
		if len(terms) > 1 {
			for i := range terms {
				terms[i] = quoteContentQuery(terms[i])
			}
			hits, err = s.searchContentMatches(ctx, q, roots, strings.Join(terms, " AND "), "terms")
			if err != nil {
				return nil, err
			}
		}
	}
	result.Truncated = len(hits) > q.Limit
	if result.Truncated {
		hits = hits[:q.Limit]
	}
	result.Hits = hits
	return result, nil
}

// SearchContent forwards typed search results for adapters that share document
// policy and indexing without round-tripping the model-facing JSON tool result.
func (t *Tools) SearchContent(ctx context.Context, q SearchQuery) (*ContentSearchResult, error) {
	if t == nil || t.store == nil {
		return nil, fmt.Errorf("document index not configured")
	}
	return t.store.SearchContent(ctx, q)
}

func (s *Store) indexBody(root string) bool {
	policy := s.rootPolicy(root)
	return policy.Indexing && policy.Context.SearchBody && policy.Context.EffectiveSearch() != RootSearchNever
}

// Keep the content index in the same transaction as its source metadata.
func (s *Store) upsertContentIndex(ctx context.Context, tx *sql.Tx, root, relPath string, doc parsedDocument) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM indexed_document_content_fts WHERE rowid =
		(SELECT rowid FROM indexed_documents WHERE root = ? AND rel_path = ?)`, root, relPath); err != nil {
		return fmt.Errorf("delete previous document content index: %w", err)
	}
	body := ""
	if s.indexBody(root) {
		body = doc.Body
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO indexed_document_content_fts(rowid, rel_path, title, summary, tags, body)
		SELECT rowid, rel_path, title, summary, ?, ? FROM indexed_documents WHERE root = ? AND rel_path = ?`,
		strings.Join(doc.Tags, " "), body, root, relPath); err != nil {
		return fmt.Errorf("index document content: %w", err)
	}
	return nil
}

// Opting out must remove indexed body bytes even if a missing file or revoked
// signature prevents the next parse. Metadata remains available for discovery.
func (s *Store) clearIndexedBody(ctx context.Context, root string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin document body removal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE indexed_document_content_fts SET body = ''
		WHERE rowid IN (SELECT rowid FROM indexed_documents WHERE root = ? AND content_body_indexed = 1)`, root); err != nil {
		return fmt.Errorf("remove opted-out document body: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE indexed_documents SET content_body_indexed = 0
		WHERE root = ? AND content_body_indexed = 1`, root); err != nil {
		return fmt.Errorf("record document body removal: %w", err)
	}
	return tx.Commit()
}

func quoteContentQuery(query string) string {
	return `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
}

func (s *Store) searchContentMatches(ctx context.Context, q SearchQuery, roots []string, match, matchType string) ([]ContentSearchHit, error) {
	args := []any{match}
	where := []string{`indexed_document_content_fts MATCH ?`, `d.root IN (` + database.Placeholders(len(roots)) + `)`}
	for _, root := range roots {
		args = append(args, root)
	}
	if q.PathPrefix != "" {
		where = append(where, `(d.rel_path = ? OR substr(d.rel_path, 1, length(?) + 1) = ? || '/')`)
		args = append(args, q.PathPrefix, q.PathPrefix, q.PathPrefix)
	}
	if !q.IncludeRestricted && !audienceExplicitlyFiltered(q) {
		where = append(where, `NOT EXISTS (SELECT 1 FROM json_each(d.frontmatter_json) fm, json_each(fm.value) av
			WHERE fm.key = 'audience' AND LOWER(TRIM(av.value)) IN (`+database.Placeholders(len(restrictedAudienceValues))+`))`)
		args = append(args, restrictedAudienceArgs()...)
	}
	for _, tag := range q.Tags {
		where = append(where, `EXISTS (SELECT 1 FROM json_each(d.tags_json) WHERE LOWER(TRIM(value)) = ?)`)
		args = append(args, strings.ToLower(strings.TrimSpace(tag)))
	}
	for _, key := range q.FrontmatterKeys {
		where = append(where, `EXISTS (SELECT 1 FROM json_each(d.frontmatter_json) WHERE key = ? AND json_array_length(value) > 0)`)
		args = append(args, strings.ToLower(strings.TrimSpace(key)))
	}
	keys := make([]string, 0, len(q.Frontmatter))
	for key := range q.Frontmatter {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		values := q.Frontmatter[key]
		where = append(where, `EXISTS (SELECT 1 FROM json_each(d.frontmatter_json) fm, json_each(fm.value) fv
			WHERE fm.key = ? AND LOWER(TRIM(fv.value)) IN (`+database.Placeholders(len(values))+`))`)
		args = append(args, key)
		for _, value := range values {
			args = append(args, strings.ToLower(strings.TrimSpace(value)))
		}
	}
	if q.ModifiedAfter != nil {
		where = append(where, `d.modified_at >= ?`)
		args = append(args, q.ModifiedAfter.UTC().Format(time.RFC3339Nano))
	}
	if q.ModifiedBefore != nil {
		where = append(where, `d.modified_at <= ?`)
		args = append(args, q.ModifiedBefore.UTC().Format(time.RFC3339Nano))
	}
	query := `SELECT d.root, d.rel_path, d.title, d.summary, d.facets_json, d.tags_json, d.frontmatter_json, d.modified_at, d.word_count,
		snippet(indexed_document_content_fts, -1, char(1), char(2), ' … ', 48)
		FROM indexed_document_content_fts JOIN indexed_documents d ON d.rowid = indexed_document_content_fts.rowid
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY bm25(indexed_document_content_fts, 2, 5, 3, 2, 1), d.modified_at DESC, d.root, d.rel_path LIMIT ?`
	args = append(args, q.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search document content: %w", err)
	}
	defer rows.Close()
	hits := make([]ContentSearchHit, 0)
	for rows.Next() {
		hit := ContentSearchHit{MatchType: matchType}
		if err := scanDocument(rows, &hit.Document, &hit.Excerpt); err != nil {
			return nil, fmt.Errorf("scan document content hit: %w", err)
		}
		hit.Excerpt = contentSearchExcerpt(hit.Excerpt)
		hit.WriteTool = documentWriteTool(firstValue(hit.Document.Frontmatter, "managed_by"), hit.Document.Facets)
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// Close the read cursor before verification, which can use the same DB.
	for _, hit := range hits {
		if err := s.verifyDocumentForConsumer(ctx, hit.Document.Root, hit.Document.Path, "document_content_search"); err != nil {
			return nil, fmt.Errorf("verify content search hit %s: %w", hit.Document.Ref, err)
		}
	}
	return hits, nil
}

func contentSearchExcerpt(marked string) string {
	firstMatch := strings.IndexByte(marked, 1)
	start := 0
	if firstMatch >= 0 {
		start = max(0, len([]rune(strings.ReplaceAll(marked[:firstMatch], "\x02", "")))-128)
	}
	plain := strings.NewReplacer("\x01", "", "\x02", "").Replace(marked)
	runes := []rune(plain)
	end := min(len(runes), start+500)
	excerpt := string(runes[start:end])
	if start > 0 {
		excerpt = "… " + excerpt
	}
	if end < len(runes) {
		excerpt += " …"
	}
	return excerpt
}
