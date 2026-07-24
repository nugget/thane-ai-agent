package documents

import "strings"

func hasFrontmatterKeys(frontmatter map[string][]string, keys []string) bool {
	if len(keys) == 0 {
		return true
	}
	for _, key := range keys {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		if len(frontmatter[key]) == 0 {
			return false
		}
	}
	return true
}

func matchesFrontmatter(frontmatter map[string][]string, required map[string][]string) bool {
	if len(required) == 0 {
		return true
	}
	for key, want := range required {
		have := frontmatter[key]
		if len(have) == 0 {
			return false
		}
		if !containsAnyFold(have, want) {
			return false
		}
	}
	return true
}

func containsAnyFold(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	set := make(map[string]bool, len(have))
	for _, value := range have {
		set[strings.ToLower(strings.TrimSpace(value))] = true
	}
	for _, value := range want {
		if set[strings.ToLower(strings.TrimSpace(value))] {
			return true
		}
	}
	return false
}

// audienceFrontmatterKey and audienceInternalValue are the documents-layer
// half of the #1250 audience contract: a document whose frontmatter
// declares audience: internal is a private working surface (loop working
// notes, process logs) rather than published content.
const (
	audienceFrontmatterKey = "audience"
	audienceInternalValue  = "internal"
)

// isInternalAudienceDocument reports whether a document declares itself
// internal-audience via frontmatter. Search excludes internal documents
// by default so process narration never leaks into consumer contexts
// through a search hit; explicit reads by ref are unaffected.
func isInternalAudienceDocument(frontmatter map[string][]string) bool {
	for _, value := range frontmatter[audienceFrontmatterKey] {
		if strings.EqualFold(strings.TrimSpace(value), audienceInternalValue) {
			return true
		}
	}
	return false
}

// audienceExplicitlyFiltered reports whether the caller's own filters
// name the audience key. An explicit audience filter is a deliberate
// selection, so the default internal exclusion steps aside instead of
// silently emptying the result. Callers pass the query after frontmatter
// normalization, so the key lookup is lowercase-safe.
func audienceExplicitlyFiltered(q SearchQuery) bool {
	if len(q.Frontmatter[audienceFrontmatterKey]) > 0 {
		return true
	}
	for _, key := range q.FrontmatterKeys {
		if strings.EqualFold(strings.TrimSpace(key), audienceFrontmatterKey) {
			return true
		}
	}
	return false
}

func normalizeSearchFrontmatter(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, values := range in {
		key = strings.ToLower(strings.TrimSpace(key))
		values = dedupeSorted(values)
		if key == "" || len(values) == 0 {
			continue
		}
		out[key] = values
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
