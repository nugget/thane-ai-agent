package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RootSummary describes one indexed document root.
type RootSummary struct {
	Root            string             `json:"root"`
	Path            string             `json:"-"`
	Policy          RootPolicySummary  `json:"policy"`
	DocumentCount   int                `json:"document_count"`
	TotalSizeBytes  int64              `json:"total_size_bytes"`
	TotalWordCount  int                `json:"total_word_count"`
	LastModifiedAt  string             `json:"last_modified_at,omitempty"`
	TopTags         []string           `json:"top_tags,omitempty"`
	TopDirectories  []BrowseDirectory  `json:"top_directories,omitempty"`
	RecentDocuments []RootDocumentHint `json:"recent_documents,omitempty"`
}

// RootDocumentHint is a compact example document attached to a root summary.
type RootDocumentHint struct {
	Ref        string `json:"ref"`
	Path       string `json:"path"`
	Title      string `json:"title"`
	ModifiedAt string `json:"modified_at"`
}

// DocumentSummary is the compact search/browse view of a document.
type DocumentSummary struct {
	Root        string              `json:"root"`
	Ref         string              `json:"ref"`
	Path        string              `json:"path"`
	Title       string              `json:"title"`
	Summary     string              `json:"summary,omitempty"`
	Tags        []string            `json:"tags,omitempty"`
	Frontmatter map[string][]string `json:"frontmatter,omitempty"`
	ModifiedAt  string              `json:"modified_at"`
	WordCount   int                 `json:"word_count"`
}

// BrowseDirectory describes one child directory in a rooted browse view.
type BrowseDirectory struct {
	Name          string `json:"name"`
	PathPrefix    string `json:"path_prefix"`
	DocumentCount int    `json:"document_count"`
}

// BrowseResult is the rooted "phone tree" view for one root/prefix.
type BrowseResult struct {
	Root        string            `json:"root"`
	PathPrefix  string            `json:"path_prefix,omitempty"`
	Directories []BrowseDirectory `json:"directories,omitempty"`
	Documents   []DocumentSummary `json:"documents,omitempty"`
}

// ValueCount counts observed frontmatter values.
type ValueCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// Store indexes managed markdown roots into the primary Thane SQLite DB.
type Store struct {
	db              *sql.DB
	roots           map[string]string
	rootPolicies    map[string]RootPolicy
	rootWriters     map[string]RootWriter
	logger          *slog.Logger
	refreshMu       sync.Mutex
	lastRefresh     time.Time
	refreshInterval time.Duration
}

const defaultRefreshInterval = 5 * time.Second

// NewStore creates a document index store backed by db.
func NewStore(db *sql.DB, roots map[string]string, logger *slog.Logger) (*Store, error) {
	return NewStoreWithOptions(db, roots, logger, StoreOptions{})
}

// NewStoreWithOptions creates a document index store backed by db and
// optional per-root policy.
func NewStoreWithOptions(db *sql.DB, roots map[string]string, logger *slog.Logger, opts StoreOptions) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("nil database")
	}
	if logger == nil {
		logger = slog.Default()
	}
	normalizedRoots := normalizeRoots(roots)
	s := &Store{
		db:              db,
		roots:           normalizedRoots,
		rootPolicies:    normalizePolicies(normalizedRoots, opts.RootPolicies),
		rootWriters:     normalizeRootWriters(normalizedRoots, opts.RootWriters),
		logger:          logger,
		refreshInterval: defaultRefreshInterval,
	}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate documents schema: %w", err)
	}
	return s, nil
}

func normalizeRoots(roots map[string]string) map[string]string {
	if len(roots) == 0 {
		return nil
	}
	out := make(map[string]string, len(roots))
	for root, dir := range roots {
		root = strings.TrimSuffix(strings.TrimSpace(root), ":")
		if root == "" || strings.TrimSpace(dir) == "" {
			continue
		}
		out[root] = filepath.Clean(dir)
	}
	return out
}

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS indexed_documents (
		root TEXT NOT NULL,
		rel_path TEXT NOT NULL,
		abs_path TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT '',
		summary TEXT NOT NULL DEFAULT '',
		tags_json TEXT NOT NULL DEFAULT '[]',
		frontmatter_json TEXT NOT NULL DEFAULT '{}',
		links_json TEXT NOT NULL DEFAULT '[]',
		modified_at TEXT NOT NULL,
		size_bytes INTEGER NOT NULL DEFAULT 0,
		word_count INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(root, rel_path)
	);
	CREATE INDEX IF NOT EXISTS idx_indexed_documents_root_path ON indexed_documents(root, rel_path);
	CREATE INDEX IF NOT EXISTS idx_indexed_documents_modified ON indexed_documents(root, modified_at DESC);

	CREATE TABLE IF NOT EXISTS indexed_document_sections (
		root TEXT NOT NULL,
		rel_path TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		level INTEGER NOT NULL,
		heading TEXT NOT NULL,
		slug TEXT NOT NULL,
		start_line INTEGER NOT NULL DEFAULT 0,
		end_line INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(root, rel_path, ordinal)
	);
	CREATE INDEX IF NOT EXISTS idx_indexed_document_sections_doc ON indexed_document_sections(root, rel_path, ordinal);
	`
	_, err := s.db.Exec(schema)
	return err
}

// Refresh incrementally refreshes all indexed roots.
func (s *Store) Refresh(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.refreshInterval > 0 && !s.lastRefresh.IsZero() && time.Since(s.lastRefresh) < s.refreshInterval {
		return nil
	}
	for root, dir := range s.roots {
		if !s.rootPolicy(root).Indexing {
			if err := s.purgeRootIndex(ctx, root); err != nil {
				return err
			}
			continue
		}
		if err := s.refreshRoot(ctx, root, dir); err != nil {
			return err
		}
	}
	s.lastRefresh = time.Now()
	return nil
}

// RunRefresher keeps the index warm in the background using the store's
// refresh interval. Errors are logged and retried on the next tick.
func (s *Store) RunRefresher(ctx context.Context) {
	if s == nil {
		return
	}
	refreshOnce := func() {
		if err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
			s.logger.Warn("document refresh failed", "error", err)
		}
	}
	refreshOnce()
	if s.refreshInterval <= 0 {
		<-ctx.Done()
		return
	}
	ticker := time.NewTicker(s.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshOnce()
		}
	}
}

func (s *Store) refreshRoot(ctx context.Context, root, dir string) error {
	scanDir, err := s.resolveRootPath(root)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	walkErr := filepath.WalkDir(scanDir, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			s.logger.Warn("document scan skipped entry", "root", root, "path", path, "error", err)
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		rel, err := filepath.Rel(scanDir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		seen[rel] = true
		if err := s.upsertFile(ctx, root, rel); err != nil {
			s.logger.Warn("document index skipped file", "root", root, "path", path, "error", err)
		}
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("scan root %q: %w", root, walkErr)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT rel_path FROM indexed_documents WHERE root = ?`, root)
	if err != nil {
		return fmt.Errorf("list indexed docs for cleanup: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var rel string
		if err := rows.Scan(&rel); err != nil {
			return fmt.Errorf("scan indexed doc for cleanup: %w", err)
		}
		if seen[rel] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM indexed_document_sections WHERE root = ? AND rel_path = ?`, root, rel); err != nil {
			return fmt.Errorf("delete stale sections: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM indexed_documents WHERE root = ? AND rel_path = ?`, root, rel); err != nil {
			return fmt.Errorf("delete stale document: %w", err)
		}
	}
	return rows.Err()
}

func (s *Store) purgeRootIndex(ctx context.Context, root string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM indexed_document_sections WHERE root = ?`, root); err != nil {
		return fmt.Errorf("delete indexed sections for non-indexed root %q: %w", root, err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM indexed_documents WHERE root = ?`, root); err != nil {
		return fmt.Errorf("delete indexed documents for non-indexed root %q: %w", root, err)
	}
	return nil
}

func (s *Store) upsertFile(ctx context.Context, root, relPath string) error {
	absPath, err := s.resolveDocumentPath(root, relPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return err
	}
	modified := info.ModTime().UTC().Format(time.RFC3339Nano)
	size := info.Size()

	var existingModified string
	var existingSize int64
	err = s.db.QueryRowContext(ctx,
		`SELECT modified_at, size_bytes FROM indexed_documents WHERE root = ? AND rel_path = ?`,
		root, relPath,
	).Scan(&existingModified, &existingSize)
	switch {
	case err == nil && existingModified == modified && existingSize == size:
		return nil
	case err != nil && err != sql.ErrNoRows:
		return fmt.Errorf("lookup indexed document: %w", err)
	}

	raw, err := os.ReadFile(absPath)
	if err != nil {
		return err
	}
	doc := parseMarkdownDocument(relPath, string(raw))
	tagsJSON, err := json.Marshal(doc.Tags)
	if err != nil {
		return fmt.Errorf("marshal tags: %w", err)
	}
	metaJSON, err := json.Marshal(doc.Frontmatter)
	if err != nil {
		return fmt.Errorf("marshal frontmatter: %w", err)
	}
	linksJSON, err := json.Marshal(doc.Links)
	if err != nil {
		return fmt.Errorf("marshal links: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin document upsert: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO indexed_documents
			(root, rel_path, abs_path, title, summary, tags_json, frontmatter_json, links_json, modified_at, size_bytes, word_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(root, rel_path) DO UPDATE SET
		 	abs_path = excluded.abs_path,
		 	title = excluded.title,
		 	summary = excluded.summary,
		 	tags_json = excluded.tags_json,
		 	frontmatter_json = excluded.frontmatter_json,
		 	links_json = excluded.links_json,
		 	modified_at = excluded.modified_at,
		 	size_bytes = excluded.size_bytes,
		 	word_count = excluded.word_count`,
		root, relPath, absPath, doc.Title, doc.Summary, string(tagsJSON), string(metaJSON), string(linksJSON), modified, size, doc.WordCount,
	); err != nil {
		return fmt.Errorf("upsert indexed document: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM indexed_document_sections WHERE root = ? AND rel_path = ?`, root, relPath); err != nil {
		return fmt.Errorf("delete old sections: %w", err)
	}
	for i, sec := range doc.Sections {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO indexed_document_sections
				(root, rel_path, ordinal, level, heading, slug, start_line, end_line)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			root, relPath, i, sec.Level, sec.Heading, sec.Slug, sec.StartLine, sec.EndLine,
		); err != nil {
			return fmt.Errorf("insert section: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Store) allRoots() []string {
	roots := make([]string, 0, len(s.roots))
	for root := range s.roots {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

func parseRef(ref string) (root string, relPath string, err error) {
	ref = strings.TrimSpace(ref)
	parts := strings.SplitN(ref, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("invalid ref %q; expected root:path.md", ref)
	}
	root = strings.TrimSuffix(strings.TrimSpace(parts[0]), ":")
	relPath = strings.TrimSpace(parts[1])
	relPath = strings.ReplaceAll(relPath, "\\", "/")
	relPath = strings.TrimPrefix(path.Clean(strings.TrimPrefix(relPath, "./")), "./")
	if relPath == "" || relPath == "." || relPath == ".." || strings.HasPrefix(relPath, "../") {
		return "", "", fmt.Errorf("invalid ref %q; path escapes root", ref)
	}
	return root, relPath, nil
}

func makeRef(root, relPath string) string {
	return root + ":" + relPath
}

func scanDocument(rows *sql.Rows, doc *DocumentSummary) error {
	var tagsJSON string
	var metaJSON string
	if err := rows.Scan(&doc.Root, &doc.Path, &doc.Title, &doc.Summary, &tagsJSON, &metaJSON, &doc.ModifiedAt, &doc.WordCount); err != nil {
		return err
	}
	doc.Ref = makeRef(doc.Root, doc.Path)
	if err := json.Unmarshal([]byte(tagsJSON), &doc.Tags); err != nil {
		doc.Tags = nil
	}
	if err := json.Unmarshal([]byte(metaJSON), &doc.Frontmatter); err != nil {
		doc.Frontmatter = nil
	}
	return nil
}

func rootExists(roots map[string]string, root string) bool {
	_, ok := roots[strings.TrimSuffix(strings.TrimSpace(root), ":")]
	return ok
}

func asValueCounts(values map[string]int) []ValueCount {
	out := make([]ValueCount, 0, len(values))
	for value, count := range values {
		out = append(out, ValueCount{Value: value, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Value < out[j].Value
		}
		return out[i].Count > out[j].Count
	})
	return out
}

func trimPathPrefix(prefix string) string {
	prefix = filepath.ToSlash(strings.Trim(strings.TrimSpace(prefix), "/"))
	if prefix == "." {
		return ""
	}
	return prefix
}

func (s *Store) resolveRootPath(root string) (string, error) {
	dir, ok := s.roots[strings.TrimSuffix(strings.TrimSpace(root), ":")]
	if !ok || strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("unknown document root %q", root)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve root %q: %w", root, err)
	}
	resolvedDir, err := filepath.EvalSymlinks(absDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("document root %q does not exist", root)
		}
		return "", fmt.Errorf("resolve root %q: %w", root, err)
	}
	return filepath.Clean(resolvedDir), nil
}

func (s *Store) resolveDocumentPath(root, relPath string) (string, error) {
	rootPath, err := s.resolveRootPath(root)
	if err != nil {
		return "", err
	}
	candidate := filepath.Clean(filepath.Join(rootPath, filepath.FromSlash(relPath)))
	if !pathWithinRoot(rootPath, candidate) {
		return "", fmt.Errorf("document path %q escapes root %q", relPath, root)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", os.ErrNotExist
		}
		return "", fmt.Errorf("resolve document path %q: %w", relPath, err)
	}
	resolved = filepath.Clean(resolved)
	if !pathWithinRoot(rootPath, resolved) {
		return "", fmt.Errorf("document path %q resolves outside root %q", relPath, root)
	}
	return resolved, nil
}

func pathWithinRoot(rootPath, targetPath string) bool {
	rel, err := filepath.Rel(rootPath, targetPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
