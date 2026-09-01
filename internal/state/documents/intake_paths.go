package documents

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

func normalizeIntakeTitle(args IntakeArgs) string {
	for _, candidate := range []string{
		args.DesiredTitle,
		firstMarkdownHeading(args.BodySnippet),
		firstSentence(args.Summary),
		firstSentence(args.ContentDigest),
	} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}
	return "Untitled Note"
}

func normalizeIntakeTags(tags []string, observed []ValueCount) []string {
	observedByFold := make(map[string]string, len(observed))
	observedBySlug := make(map[string]string, len(observed))
	for _, value := range observed {
		clean := strings.TrimSpace(value.Value)
		if clean == "" {
			continue
		}
		observedByFold[strings.ToLower(clean)] = clean
		if slug := slugifyIntakeValue(clean); slug != "" {
			observedBySlug[slug] = clean
		}
	}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if observed := observedByFold[strings.ToLower(tag)]; observed != "" {
			out = append(out, observed)
			continue
		}
		slug := slugifyIntakeValue(tag)
		if slug == "" {
			continue
		}
		if observed := observedBySlug[slug]; observed != "" {
			out = append(out, observed)
			continue
		}
		out = append(out, slug)
	}
	return dedupeSorted(out)
}

func slugifyIntakeValue(raw string) string {
	for _, r := range raw {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return slugify(raw)
		}
	}
	return ""
}

func (s *Store) proposeIntakeRef(ctx context.Context, root string, args IntakeArgs, title string, tags []string, related []IntakeRelatedDocument) (string, string, error) {
	if err := ValidateIntakePlacement(root, args.DesiredRef, args.PathPrefix); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(args.DesiredRef) != "" {
		desiredRoot, relPath, err := parseRef(args.DesiredRef)
		if err != nil {
			return "", "", fmt.Errorf("invalid document ref: %w", err)
		}
		if normalizeRootName(desiredRoot) != root {
			return "", "", fmt.Errorf("document ref root %q does not match requested root %q", desiredRoot, root)
		}
		return makeRef(root, relPath), relPath, nil
	}

	dir := trimPathPrefix(args.PathPrefix)
	if dir == "" {
		if top := topRelated(related); top != nil && top.Score >= intakeMaybeOverlapScore {
			dir = path.Dir(top.Path)
			if dir == "." {
				dir = ""
			}
		}
	}
	if dir == "" && len(tags) > 0 {
		dir = slugify(tags[0])
	}
	if dir == "" {
		dir = "notes"
	}
	slug := slugify(title)
	relPath, err := s.uniqueIntakePath(ctx, root, dir, slug)
	if err != nil {
		return "", "", err
	}
	return makeRef(root, relPath), relPath, nil
}

// ValidateIntakePlacement enforces root-owned placement invariants before
// corpus-aware intake proposes or commits a destination. The dossiers root is
// a flat subject catalog whose names must be chosen deliberately after sibling
// inspection, so it requires an explicit direct-child ref and never accepts a
// path prefix.
func ValidateIntakePlacement(root, desiredRef, pathPrefix string) error {
	root = normalizeRootName(root)
	desiredRef = strings.TrimSpace(desiredRef)

	var (
		refRoot string
		relPath string
		refErr  error
	)
	if desiredRef != "" {
		refRoot, relPath, refErr = parseRef(desiredRef)
	}
	isDossiers := root == "dossiers" || (refErr == nil && normalizeRootName(refRoot) == "dossiers")
	if !isDossiers {
		return nil
	}
	if trimPathPrefix(pathPrefix) != "" {
		return fmt.Errorf("dossiers is a flat subject catalog; omit path_prefix, inspect sibling refs, and use an explicit direct-child ref such as dossiers:entity-cat-goro-goro.md")
	}
	if desiredRef == "" {
		return fmt.Errorf("dossiers requires an explicit direct-child document ref chosen after inspecting sibling refs, such as dossiers:entity-cat-goro-goro.md")
	}
	if refErr != nil {
		return fmt.Errorf("invalid dossiers document ref: %w", refErr)
	}
	refRoot = normalizeRootName(refRoot)
	if root == "" {
		root = refRoot
	}
	if refRoot != root {
		return fmt.Errorf("document ref root %q does not match requested root %q", refRoot, root)
	}
	return validateNewDocumentPlacement(root, relPath)
}

func validateNewDocumentPlacement(root, relPath string) error {
	if normalizeRootName(root) == "dossiers" && strings.Contains(filepath.ToSlash(relPath), "/") {
		return fmt.Errorf("dossiers accepts direct-child refs only; inspect sibling naming and retry with a flat ref such as dossiers:entity-cat-goro-goro.md")
	}
	return nil
}

func (s *Store) uniqueIntakePath(ctx context.Context, root, dir, slug string) (string, error) {
	dir = trimPathPrefix(dir)
	slug = slugify(slug)
	if slug == "" {
		slug = "note"
	}
	for i := 0; i < 100; i++ {
		name := slug
		if i > 0 {
			name = fmt.Sprintf("%s-%d", slug, i+1)
		}
		relPath := name + ".md"
		if dir != "" {
			relPath = path.Join(dir, relPath)
		}
		if !s.refExists(ctx, root, relPath) {
			return relPath, nil
		}
	}
	return "", fmt.Errorf("could not allocate a unique path for %q in root %q", slug, root)
}

func (s *Store) refExists(ctx context.Context, root, relPath string) bool {
	if exists, err := s.documentExists(ctx, root, relPath); err == nil && exists {
		return true
	}
	absPath, err := s.resolveDocumentWritePath(root, relPath)
	if err != nil {
		return false
	}
	if _, err := os.Stat(absPath); err == nil {
		return true
	}
	return false
}

func topRelated(related []IntakeRelatedDocument) *IntakeRelatedDocument {
	if len(related) == 0 {
		return nil
	}
	return &related[0]
}

func firstMarkdownHeading(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		if heading != "" {
			return heading
		}
	}
	return ""
}

func firstSentence(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	for _, sep := range []string{".", "\n"} {
		if idx := strings.Index(raw, sep); idx > 0 {
			raw = raw[:idx]
			break
		}
	}
	words := strings.Fields(raw)
	if len(words) > 10 {
		words = words[:10]
	}
	return strings.Join(words, " ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func intakeLooksAppendOrJournal(intent string) bool {
	intent = strings.ToLower(intent)
	for _, word := range []string{"append", "journal", "log", "entry", "note"} {
		if strings.Contains(intent, word) {
			return true
		}
	}
	return false
}
