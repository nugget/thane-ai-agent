package documents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DocumentRecord is the full model-facing view of one managed document.
type DocumentRecord struct {
	Root        string              `json:"root"`
	Ref         string              `json:"ref"`
	Path        string              `json:"path"`
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Tags        []string            `json:"tags,omitempty"`
	Frontmatter map[string][]string `json:"frontmatter,omitempty"`
	Body        string              `json:"body"`
	Outline     []Section           `json:"outline,omitempty"`
	ModifiedAt  string              `json:"modified_at"`
	WordCount   int                 `json:"word_count"`
	SizeBytes   int64               `json:"size_bytes"`
}

// WriteArgs creates or replaces a whole managed document.
type WriteArgs struct {
	Ref          string              `json:"ref"`
	Title        string              `json:"title,omitempty"`
	Description  string              `json:"description,omitempty"`
	Tags         []string            `json:"tags,omitempty"`
	Frontmatter  map[string][]string `json:"frontmatter,omitempty"`
	Body         *string             `json:"body,omitempty"`
	JournalEntry string              `json:"journal_entry,omitempty"`
}

// EditArgs updates part of a managed document without leaving the
// semantic document abstraction.
type EditArgs struct {
	Ref         string              `json:"ref"`
	Mode        string              `json:"mode"`
	Content     string              `json:"content,omitempty"`
	Section     string              `json:"section,omitempty"`
	Heading     string              `json:"heading,omitempty"`
	Level       int                 `json:"level,omitempty"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Tags        []string            `json:"tags,omitempty"`
	Frontmatter map[string][]string `json:"frontmatter,omitempty"`
}

// JournalUpdateArgs appends a timestamped note into a rolling window
// journal document while keeping window headings and timestamps stable.
type JournalUpdateArgs struct {
	Ref          string              `json:"ref"`
	Entry        string              `json:"entry"`
	Window       string              `json:"window,omitempty"`
	MaxWindows   int                 `json:"max_windows,omitempty"`
	HeadingLevel int                 `json:"heading_level,omitempty"`
	Title        string              `json:"title,omitempty"`
	Description  string              `json:"description,omitempty"`
	Tags         []string            `json:"tags,omitempty"`
	Frontmatter  map[string][]string `json:"frontmatter,omitempty"`
}

// MutationResult summarizes one managed document write/edit.
type MutationResult struct {
	Action      string   `json:"action"`
	Ref         string   `json:"ref"`
	Root        string   `json:"root"`
	Path        string   `json:"path"`
	Existed     bool     `json:"existed"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	ModifiedAt  string   `json:"modified_at"`
	WordCount   int      `json:"word_count"`
	SizeBytes   int64    `json:"size_bytes"`
	Section     string   `json:"section,omitempty"`
	Window      string   `json:"window,omitempty"`
}

func (s *Store) Read(ctx context.Context, ref string) (*DocumentRecord, error) {
	root, relPath, err := parseRef(ref)
	if err != nil {
		return nil, err
	}
	absPath, err := s.resolveDocumentPath(root, relPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", ref)
		}
		return nil, err
	}
	record, _, _, err := s.readDocumentFile(absPath, root, relPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", ref)
		}
		return nil, err
	}
	return record, nil
}

func (s *Store) Write(ctx context.Context, args WriteArgs) (*MutationResult, error) {
	root, relPath, err := parseRef(args.Ref)
	if err != nil {
		return nil, err
	}
	absPath, err := s.resolveDocumentWritePath(root, relPath)
	if err != nil {
		return nil, err
	}

	existed := false
	var existingRecord *DocumentRecord
	if _, err := os.Stat(absPath); err == nil {
		existed = true
		record, _, _, readErr := s.readDocumentFile(absPath, root, relPath)
		if readErr != nil {
			return nil, readErr
		}
		existingRecord = record
	}

	now := time.Now()
	body := ""
	if args.Body != nil {
		body = *args.Body
	} else if existingRecord != nil {
		body = existingRecord.Body
	}
	sectionName := ""
	if strings.TrimSpace(args.JournalEntry) != "" {
		body, err = upsertDocumentJournal(body, existingRecord, now, args.JournalEntry)
		if err != nil {
			return nil, err
		}
		sectionName = documentJournalHeading
	}
	meta := mergeDocumentFrontmatter(existingRecord, args.Title, args.Description, args.Tags, args.Frontmatter, now)
	raw := renderDocument(meta, body)

	if err := s.writeDocumentFile(ctx, root, relPath, raw); err != nil {
		return nil, err
	}
	record, _, _, err := s.readDocumentFile(absPath, root, relPath)
	if err != nil {
		return nil, err
	}
	return mutationResultFromRecord("doc_write", record, existed, sectionName, ""), nil
}

func (s *Store) Edit(ctx context.Context, args EditArgs) (*MutationResult, error) {
	root, relPath, err := parseRef(args.Ref)
	if err != nil {
		return nil, err
	}
	absPath, err := s.resolveDocumentPath(root, relPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", args.Ref)
		}
		return nil, err
	}

	record, rawFrontmatter, body, err := s.readDocumentFile(absPath, root, relPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", args.Ref)
		}
		return nil, err
	}

	mode := strings.ToLower(strings.TrimSpace(args.Mode))
	if mode == "" {
		return nil, fmt.Errorf("mode is required")
	}
	editedBody := body
	sectionName := ""
	switch mode {
	case "metadata":
		// metadata-only update
	case "replace_body":
		editedBody = trimDocumentBody(args.Content)
	case "append_body":
		editedBody = appendDocumentBody(body, args.Content)
	case "prepend_body":
		editedBody = prependDocumentBody(body, args.Content)
	case "upsert_section":
		editedBody, sectionName, err = upsertDocumentSection(body, args.Section, args.Heading, args.Level, args.Content)
		if err != nil {
			return nil, err
		}
	case "delete_section":
		editedBody, sectionName, err = deleteDocumentSection(body, args.Section)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported edit mode %q; use one of [metadata, replace_body, append_body, prepend_body, upsert_section, delete_section]", args.Mode)
	}

	raw := rawFrontmatter
	if len(args.Frontmatter) > 0 || args.Title != "" || args.Description != "" || len(args.Tags) > 0 {
		meta := mergeDocumentFrontmatter(record, args.Title, args.Description, args.Tags, args.Frontmatter, time.Now())
		raw = renderFrontmatter(meta)
	} else {
		raw = touchDocumentFrontmatter(raw, record, time.Now())
	}
	rendered := renderDocumentFromParts(raw, editedBody)
	if err := s.writeDocumentFile(ctx, root, relPath, rendered); err != nil {
		return nil, err
	}
	record, _, _, err = s.readDocumentFile(absPath, root, relPath)
	if err != nil {
		return nil, err
	}
	return mutationResultFromRecord("doc_edit", record, true, sectionName, ""), nil
}

func (s *Store) JournalUpdate(ctx context.Context, args JournalUpdateArgs) (*MutationResult, error) {
	root, relPath, err := parseRef(args.Ref)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Entry) == "" {
		return nil, fmt.Errorf("entry is required")
	}

	absPath, err := s.resolveDocumentWritePath(root, relPath)
	if err != nil {
		return nil, err
	}
	existed := false
	var record *DocumentRecord
	var rawFrontmatter string
	var body string
	if _, err := os.Stat(absPath); err == nil {
		existed = true
		record, rawFrontmatter, body, err = s.readDocumentFile(absPath, root, relPath)
		if err != nil {
			return nil, err
		}
	}

	now := time.Now()
	windowKind := normalizeJournalWindow(args.Window)
	windowHeading := journalWindowHeading(now, windowKind)
	entryText := formatJournalEntry(now, args.Entry)
	level := args.HeadingLevel
	if level <= 0 {
		level = 2
	}
	maxWindows := args.MaxWindows
	if maxWindows <= 0 {
		maxWindows = defaultJournalWindowLimit(windowKind)
	}

	var updatedBody string
	if existed {
		sectionBody := currentSectionBody(body, windowHeading)
		combined := appendJournalEntryBody(sectionBody, entryText)
		updatedBody, _, err = upsertDocumentSection(body, windowHeading, windowHeading, level, combined)
		if err != nil {
			return nil, err
		}
	} else {
		combined := appendJournalEntryBody("", entryText)
		updatedBody, _, err = upsertDocumentSection("", windowHeading, windowHeading, level, combined)
		if err != nil {
			return nil, err
		}
	}
	updatedBody = pruneJournalWindows(updatedBody, level, windowKind, maxWindows)

	var rendered string
	if existed && args.Title == "" && args.Description == "" && len(args.Tags) == 0 && len(args.Frontmatter) == 0 {
		rendered = renderDocumentFromParts(touchDocumentFrontmatter(rawFrontmatter, record, now), updatedBody)
	} else {
		meta := mergeDocumentFrontmatter(record, args.Title, args.Description, args.Tags, args.Frontmatter, now)
		rendered = renderDocument(meta, updatedBody)
	}
	if err := s.writeDocumentFile(ctx, root, relPath, rendered); err != nil {
		return nil, err
	}
	record, _, _, err = s.readDocumentFile(absPath, root, relPath)
	if err != nil {
		return nil, err
	}
	return mutationResultFromRecord("doc_journal_update", record, existed, windowHeading, windowKind), nil
}

func (s *Store) readDocumentFile(absPath, root, relPath string) (*DocumentRecord, string, string, error) {
	rawBytes, err := os.ReadFile(absPath)
	if err != nil {
		return nil, "", "", err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, "", "", err
	}
	raw := string(rawBytes)
	rawFrontmatter, body, hasFrontmatter := splitFrontmatterBlock(raw)
	meta, strippedBody := splitFrontmatter(raw)
	if !hasFrontmatter {
		strippedBody = raw
	}
	doc := parseMarkdownDocumentParts(relPath, meta, strippedBody)
	return &DocumentRecord{
		Root:        root,
		Ref:         makeRef(root, relPath),
		Path:        relPath,
		Title:       doc.Title,
		Description: firstValue(doc.Frontmatter, "description"),
		Tags:        append([]string(nil), doc.Tags...),
		Frontmatter: cloneFrontmatter(doc.Frontmatter),
		Body:        strippedBody,
		Outline:     append([]Section(nil), doc.Sections...),
		ModifiedAt:  info.ModTime().UTC().Format(time.RFC3339Nano),
		WordCount:   doc.WordCount,
		SizeBytes:   info.Size(),
	}, rawFrontmatter, body, nil
}

func (s *Store) writeDocumentFile(ctx context.Context, root, relPath, raw string) error {
	absPath, err := s.resolveDocumentWritePath(root, relPath)
	if err != nil {
		return err
	}
	if err := s.ensureRootAuthoringAllowed(root); err != nil {
		return err
	}
	if writer := s.rootWriter(root); writer != nil {
		if err := writer.Write(ctx, relPath, raw, documentMutationMessage("doc_write", root, relPath)); err != nil {
			return fmt.Errorf("write document through root policy: %w", err)
		}
		if err := s.refreshDocumentWrite(ctx, root, relPath); err != nil {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("create document directories: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(absPath), ".thane-doc-*")
	if err != nil {
		return fmt.Errorf("create temp document: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp document: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp document: %w", err)
	}
	if err := os.Rename(tmpPath, absPath); err != nil {
		return fmt.Errorf("replace document: %w", err)
	}
	if err := s.refreshDocumentWrite(ctx, root, relPath); err != nil {
		return err
	}
	return nil
}

func (s *Store) refreshDocumentWrite(ctx context.Context, root, relPath string) error {
	if !s.rootPolicy(root).Indexing {
		if err := s.deleteIndexedDocument(ctx, root, relPath); err != nil {
			return err
		}
		s.touchLastRefresh(time.Now())
		return nil
	}
	if err := s.upsertFile(ctx, root, relPath); err != nil {
		return fmt.Errorf("refresh indexed document: %w", err)
	}
	s.touchLastRefresh(time.Now())
	return nil
}

func (s *Store) removeDocumentFile(ctx context.Context, root, relPath string) error {
	absPath, err := s.resolveDocumentPath(root, relPath)
	if err != nil {
		return err
	}
	if err := s.ensureRootAuthoringAllowed(root); err != nil {
		return err
	}
	if writer := s.rootWriter(root); writer != nil {
		if err := writer.Delete(ctx, relPath, documentMutationMessage("doc_delete", root, relPath)); err != nil {
			return fmt.Errorf("delete document through root policy: %w", err)
		}
	} else if err := os.Remove(absPath); err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	if err := s.deleteIndexedDocument(ctx, root, relPath); err != nil {
		return err
	}
	if rootPath, err := s.resolveRootPath(root); err == nil {
		s.pruneEmptyDocumentDirs(rootPath, filepath.Dir(absPath))
	}
	return nil
}

func (s *Store) ensureRootAuthoringAllowed(root string) error {
	mode := s.rootPolicy(root).Authoring
	switch mode {
	case "", AuthoringManaged:
		return nil
	case AuthoringReadOnly, AuthoringRestricted:
		return fmt.Errorf("document root %q authoring is %q; managed mutations are not allowed", root, mode)
	default:
		return fmt.Errorf("document root %q has unsupported authoring mode %q", root, mode)
	}
}

func documentMutationMessage(action, root, relPath string) string {
	return action + " " + makeRef(root, relPath)
}

func (s *Store) resolveDocumentWritePath(root, relPath string) (string, error) {
	rootPath, err := s.resolveRootPath(root)
	if err != nil {
		return "", err
	}
	candidate := filepath.Clean(filepath.Join(rootPath, filepath.FromSlash(relPath)))
	if !pathWithinRoot(rootPath, candidate) {
		return "", fmt.Errorf("document path %q escapes root %q", relPath, root)
	}
	checkPath := candidate
	for {
		if _, err := os.Lstat(checkPath); err == nil {
			break
		}
		parent := filepath.Dir(checkPath)
		if parent == checkPath {
			break
		}
		checkPath = parent
	}
	resolved, err := filepath.EvalSymlinks(checkPath)
	if err != nil {
		return "", fmt.Errorf("resolve document path %q: %w", relPath, err)
	}
	resolved = filepath.Clean(resolved)
	if !pathWithinRoot(rootPath, resolved) {
		return "", fmt.Errorf("document path %q resolves outside root %q", relPath, root)
	}
	return candidate, nil
}

func mergeDocumentFrontmatter(existing *DocumentRecord, title, description string, tags []string, patch map[string][]string, now time.Time) map[string][]string {
	meta := map[string][]string{}
	if existing != nil {
		meta = cloneFrontmatter(existing.Frontmatter)
	}
	for key, values := range patch {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		meta[key] = normalizeFrontmatterValues(values)
	}
	if title != "" {
		meta["title"] = []string{title}
	}
	if description != "" {
		meta["description"] = []string{description}
	}
	if len(tags) > 0 {
		meta["tags"] = normalizeFrontmatterValues(tags)
	}
	created := firstValue(meta, "created")
	if created == "" {
		created = now.Format(time.RFC3339)
	}
	meta["created"] = []string{created}
	meta["updated"] = []string{now.Format(time.RFC3339)}
	return meta
}

func mutationResultFromRecord(action string, record *DocumentRecord, existed bool, section, window string) *MutationResult {
	return &MutationResult{
		Action:      action,
		Ref:         record.Ref,
		Root:        record.Root,
		Path:        record.Path,
		Existed:     existed,
		Title:       record.Title,
		Description: record.Description,
		Tags:        append([]string(nil), record.Tags...),
		CreatedAt:   firstValue(record.Frontmatter, "created"),
		UpdatedAt:   firstValue(record.Frontmatter, "updated"),
		ModifiedAt:  record.ModifiedAt,
		WordCount:   record.WordCount,
		SizeBytes:   record.SizeBytes,
		Section:     section,
		Window:      window,
	}
}

func splitFrontmatterBlock(raw string) (string, string, bool) {
	if !strings.HasPrefix(raw, "---") {
		return "", raw, false
	}
	rest := strings.TrimLeft(raw[3:], " \t")
	switch {
	case strings.HasPrefix(rest, "\r\n"):
		rest = rest[2:]
	case strings.HasPrefix(rest, "\n"):
		rest = rest[1:]
	default:
		return "", raw, false
	}
	closeIdx, closeLen := findFrontmatterClose(rest)
	if closeIdx < 0 {
		return "", raw, false
	}
	metaRaw := rest[:closeIdx]
	body := strings.TrimLeft(rest[closeIdx+closeLen:], "\r\n")
	return metaRaw, body, true
}
