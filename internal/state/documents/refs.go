package documents

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

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
