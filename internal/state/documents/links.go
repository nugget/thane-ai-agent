package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

type documentIndexEntry struct {
	Root       string
	Path       string
	Ref        string
	Title      string
	ModifiedAt string
	Links      []string
}

type documentIndex struct {
	byRef        map[string]documentIndexEntry
	byRootTitles map[string]map[string][]documentIndexEntry
	roots        map[string]bool
}

// Links returns outgoing links, backlinks, or both for one indexed document.
func (s *Store) Links(ctx context.Context, ref string, mode string, limit int, perBacklinkLimit int) (*LinksResult, error) {
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}
	root, relPath, err := parseRef(ref)
	if err != nil {
		return nil, err
	}
	if err := s.verifyDocumentForConsumer(ctx, root, relPath, "doc_links"); err != nil {
		return nil, err
	}
	mode, err = normalizeLinkMode(mode)
	if err != nil {
		return nil, err
	}
	includeLinks := mode != "outgoing"
	index, err := s.loadDocumentIndex(ctx, includeLinks)
	if err != nil {
		return nil, err
	}
	canonicalRef := makeRef(root, relPath)
	entry, ok := index.byRef[canonicalRef]
	if !ok {
		return nil, fmt.Errorf("document not found: %s", ref)
	}
	if mode == "outgoing" {
		links, err := s.loadDocumentLinks(ctx, entry.Root, entry.Path)
		if err != nil {
			return nil, err
		}
		entry.Links = links
	}

	result := &LinksResult{
		Ref:              entry.Ref,
		Mode:             mode,
		Limit:            limit,
		PerBacklinkLimit: perBacklinkLimit,
	}
	if mode != "backlinks" {
		outgoing := make([]DocumentLink, 0, len(entry.Links))
		for _, target := range entry.Links {
			outgoing = append(outgoing, resolveDocumentLink(index, entry, target))
		}
		if limit > 0 && len(outgoing) > limit {
			result.OutgoingTruncated = true
			outgoing = outgoing[:limit]
		}
		result.Outgoing = outgoing
	}
	if mode != "outgoing" {
		result.Backlinks, result.BacklinksTruncated = backlinksForRef(index, entry.Ref, limit, perBacklinkLimit)
	}
	return result, nil
}

func (s *Store) loadDocumentIndex(ctx context.Context, includeLinks bool) (*documentIndex, error) {
	query := `SELECT root, rel_path, title, modified_at FROM indexed_documents ORDER BY root, rel_path`
	if includeLinks {
		query = `SELECT root, rel_path, title, modified_at, links_json
		 FROM indexed_documents
		 ORDER BY root, rel_path`
	}
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query document link index: %w", err)
	}
	defer rows.Close()

	index := &documentIndex{
		byRef:        make(map[string]documentIndexEntry),
		byRootTitles: make(map[string]map[string][]documentIndexEntry),
		roots:        make(map[string]bool, len(s.roots)),
	}
	for root := range s.roots {
		index.roots[root] = true
	}
	for rows.Next() {
		var (
			root       string
			relPath    string
			title      string
			modifiedAt string
			linksJSON  string
		)
		if includeLinks {
			if err := rows.Scan(&root, &relPath, &title, &modifiedAt, &linksJSON); err != nil {
				return nil, fmt.Errorf("scan document link index: %w", err)
			}
		} else if err := rows.Scan(&root, &relPath, &title, &modifiedAt); err != nil {
			return nil, fmt.Errorf("scan document link index: %w", err)
		}
		if err := s.verifyDocumentForConsumer(ctx, root, relPath, "doc_links_index"); err != nil {
			s.logger.Warn("document links skipped file blocked by signature policy",
				"root", root, "path", relPath, "error", err)
			continue
		}
		var links []string
		if includeLinks {
			links, err = decodeDocumentLinks(root, relPath, linksJSON)
			if err != nil {
				return nil, err
			}
		}
		entry := documentIndexEntry{
			Root:       root,
			Path:       relPath,
			Ref:        makeRef(root, relPath),
			Title:      title,
			ModifiedAt: modifiedAt,
			Links:      links,
		}
		index.byRef[entry.Ref] = entry
		titleKey := strings.ToLower(strings.TrimSpace(title))
		if titleKey == "" {
			continue
		}
		if index.byRootTitles[root] == nil {
			index.byRootTitles[root] = make(map[string][]documentIndexEntry)
		}
		index.byRootTitles[root][titleKey] = append(index.byRootTitles[root][titleKey], entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return index, nil
}

func (s *Store) loadDocumentLinks(ctx context.Context, root string, relPath string) ([]string, error) {
	var linksJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT links_json FROM indexed_documents WHERE root = ? AND rel_path = ?`,
		root, relPath,
	).Scan(&linksJSON)
	if err != nil {
		return nil, fmt.Errorf("query document links for %s/%s: %w", root, relPath, err)
	}
	return decodeDocumentLinks(root, relPath, linksJSON)
}

func decodeDocumentLinks(root string, relPath string, linksJSON string) ([]string, error) {
	var links []string
	if err := json.Unmarshal([]byte(linksJSON), &links); err != nil {
		return nil, fmt.Errorf("unmarshal document links for %s/%s: %w", root, relPath, err)
	}
	return links, nil
}

func backlinksForRef(index *documentIndex, targetRef string, limit int, perBacklinkLimit int) ([]Backlink, bool) {
	if index == nil {
		return nil, false
	}
	bySource := make(map[string]*Backlink)
	for _, entry := range index.byRef {
		if entry.Ref == targetRef {
			continue
		}
		for _, rawTarget := range entry.Links {
			resolved := resolveDocumentLink(index, entry, rawTarget)
			if resolved.Ref != targetRef {
				continue
			}
			backlink := bySource[entry.Ref]
			if backlink == nil {
				backlink = &Backlink{
					Ref:        entry.Ref,
					Path:       entry.Path,
					Title:      entry.Title,
					ModifiedAt: entry.ModifiedAt,
				}
				bySource[entry.Ref] = backlink
			}
			backlink.Targets = append(backlink.Targets, rawTarget)
		}
	}
	if len(bySource) == 0 {
		return nil, false
	}
	out := make([]Backlink, 0, len(bySource))
	for _, backlink := range bySource {
		backlink.Targets = dedupeSorted(backlink.Targets)
		if perBacklinkLimit > 0 && len(backlink.Targets) > perBacklinkLimit {
			backlink.Targets = backlink.Targets[:perBacklinkLimit]
			backlink.TargetsTruncated = true
		}
		out = append(out, *backlink)
	}
	sort.Slice(out, func(i, j int) bool {
		ti, _ := database.ParseTimestamp(out[i].ModifiedAt)
		tj, _ := database.ParseTimestamp(out[j].ModifiedAt)
		if ti.Equal(tj) {
			return out[i].Ref < out[j].Ref
		}
		return ti.After(tj)
	})
	truncated := false
	if limit > 0 && len(out) > limit {
		truncated = true
		out = out[:limit]
	}
	return out, truncated
}

func resolveDocumentLink(index *documentIndex, source documentIndexEntry, rawTarget string) DocumentLink {
	link := DocumentLink{Target: rawTarget}
	target, anchor := splitResolvedLinkTarget(rawTarget)
	if anchor != "" {
		link.Anchor = anchor
	}
	if target == "" {
		link.Kind = "section"
		link.Ref = source.Ref
		link.Title = source.Title
		return link
	}
	if root, _, err := parseRef(target); err == nil && index.roots[root] {
		if ref, title, ok := resolveIndexedRef(index, source, target); ok {
			link.Ref = ref
			link.Title = title
			if anchor != "" {
				link.Kind = "section"
			} else {
				link.Kind = "document"
			}
			return link
		}
		link.Kind = "unresolved"
		return link
	}
	if ref, title, ok := resolveIndexedRef(index, source, target); ok {
		link.Ref = ref
		link.Title = title
		if anchor != "" {
			link.Kind = "section"
		} else {
			link.Kind = "document"
		}
		return link
	}
	if hasExternalLinkScheme(target) {
		link.Kind = "external"
		link.URL = rawTarget
		return link
	}
	link.Kind = "unresolved"
	return link
}

func resolveIndexedRef(index *documentIndex, source documentIndexEntry, target string) (string, string, bool) {
	if index == nil {
		return "", "", false
	}
	if root, relPath, err := parseRef(target); err == nil {
		ref := makeRef(root, relPath)
		if entry, ok := index.byRef[ref]; ok {
			return ref, entry.Title, true
		}
		return "", "", false
	}

	for _, candidate := range linkPathCandidates(source.Path, target) {
		ref := makeRef(source.Root, candidate)
		if entry, ok := index.byRef[ref]; ok {
			return ref, entry.Title, true
		}
	}

	titleKey := strings.ToLower(strings.TrimSpace(target))
	if titleKey == "" {
		return "", "", false
	}
	matches := index.byRootTitles[source.Root][titleKey]
	if len(matches) != 1 {
		return "", "", false
	}
	return matches[0].Ref, matches[0].Title, true
}

func splitResolvedLinkTarget(raw string) (string, string) {
	target := strings.TrimSpace(raw)
	if pipe := strings.Index(target, "|"); pipe >= 0 {
		target = strings.TrimSpace(target[:pipe])
	}
	anchor := ""
	if cut := strings.Index(target, "#"); cut >= 0 {
		anchor = strings.TrimSpace(target[cut+1:])
		target = strings.TrimSpace(target[:cut])
	}
	if cut := strings.Index(target, "?"); cut >= 0 {
		target = strings.TrimSpace(target[:cut])
	}
	return target, anchor
}

func linkPathCandidates(sourcePath, target string) []string {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	var candidates []string
	seen := make(map[string]bool)
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		candidate = path.Clean(strings.TrimPrefix(candidate, "./"))
		candidate = strings.TrimPrefix(candidate, "/")
		if candidate == "" || candidate == "." {
			return
		}
		if seen[candidate] {
			return
		}
		seen[candidate] = true
		candidates = append(candidates, candidate)
	}

	if strings.HasPrefix(target, "/") {
		add(target)
		if path.Ext(target) == "" {
			add(target + ".md")
		}
		return candidates
	}

	dir := path.Dir(sourcePath)
	if dir == "." {
		dir = ""
	}
	if dir == "" {
		add(target)
	} else {
		add(path.Join(dir, target))
	}
	if path.Ext(target) == "" {
		if dir == "" {
			add(target + ".md")
		} else {
			add(path.Join(dir, target+".md"))
		}
	}
	add(target)
	if path.Ext(target) == "" {
		add(target + ".md")
	}
	return candidates
}

func hasExternalLinkScheme(target string) bool {
	lower := strings.ToLower(strings.TrimSpace(target))
	if strings.HasPrefix(lower, "//") || strings.Contains(lower, "://") {
		return true
	}
	for _, prefix := range []string{"mailto:", "tel:", "sms:", "signal:", "data:", "file:"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func normalizeLinkMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "both":
		return "both", nil
	case "outgoing":
		return "outgoing", nil
	case "backlinks":
		return "backlinks", nil
	default:
		return "", fmt.Errorf("unsupported mode %q; use both, outgoing, or backlinks", mode)
	}
}
