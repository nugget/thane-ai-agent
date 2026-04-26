package documents

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
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
		observedBySlug[slugify(clean)] = clean
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
		slug := slugify(tag)
		if observed := observedBySlug[slug]; observed != "" {
			out = append(out, observed)
			continue
		}
		out = append(out, slug)
	}
	return dedupeSorted(out)
}

func (s *Store) proposeIntakeRef(ctx context.Context, root string, args IntakeArgs, title string, tags []string, related []IntakeRelatedDocument) (string, string, error) {
	if strings.TrimSpace(args.DesiredRef) != "" {
		desiredRoot, relPath, err := parseRef(args.DesiredRef)
		if err != nil {
			return "", "", fmt.Errorf("desired_ref: %w", err)
		}
		if normalizeRootName(desiredRoot) != root {
			return "", "", fmt.Errorf("desired_ref root %q does not match intake root %q", desiredRoot, root)
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
