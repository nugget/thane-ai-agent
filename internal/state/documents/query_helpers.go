package documents

import (
	"sort"
	"strings"
)

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

// audienceFrontmatterKey is the documents-layer half of the #1250
// audience contract: frontmatter declares how far a document reaches
// inside this Thane instance.
const audienceFrontmatterKey = "audience"

// restrictedAudienceValues are the reaches narrower than agent-wide. A
// document declaring one is never advertised and never returned by a
// search; explicit reads by ref are unaffected, so this is context
// hygiene rather than access control.
//
// "internal" is the retired spelling of "private" and still parses,
// because frontmatter written before the rename is on disk.
var restrictedAudienceValues = []string{"private", "internal", "subscribers"}

// isRestrictedAudienceDocument reports whether a document declares a
// reach narrower than agent-wide. An absent audience is agent-wide, so
// documents that are not loop outputs keep advertising as they always
// have.
func isRestrictedAudienceDocument(frontmatter map[string][]string) bool {
	for _, value := range frontmatter[audienceFrontmatterKey] {
		trimmed := strings.TrimSpace(value)
		for _, restricted := range restrictedAudienceValues {
			if strings.EqualFold(trimmed, restricted) {
				return true
			}
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

// searchExcludedRoots returns the roots whose documents must not appear
// in a search, given the root the query named (empty for an unscoped
// search). A root set to on_request stays out of an unscoped query but
// is fully reachable once the query names it — the shape a large foreign
// corpus wants, where the documents are worth having but would drown an
// open-ended search.
//
// The result feeds a SQL NOT IN clause so an excluded corpus is never
// fetched, decoded, or scored: a root that was not asked for should cost
// nothing, which is not true of a filter applied after the scan. Order
// is deterministic to keep the generated query stable across calls.
func (s *Store) searchExcludedRoots(namedRoot string) []string {
	if s == nil {
		return nil
	}
	named := normalizeRootName(namedRoot)
	var excluded []string
	for _, root := range s.allRoots() {
		switch s.rootPolicy(root).Context.EffectiveSearch() {
		case RootSearchNever:
			excluded = append(excluded, root)
		case RootSearchOnRequest:
			if named != root {
				excluded = append(excluded, root)
			}
		}
	}
	sort.Strings(excluded)
	return excluded
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

// restrictedAudienceArgs returns restrictedAudienceValues as query
// arguments. The SQL gate names one placeholder per value, so the two
// must stay the same length.
func restrictedAudienceArgs() []any {
	args := make([]any, 0, len(restrictedAudienceValues))
	for _, value := range restrictedAudienceValues {
		args = append(args, value)
	}
	return args
}

// IsRestrictedAudience reports whether a single frontmatter audience
// value declares a reach narrower than agent-wide. Exported for callers
// outside this package that gate on the same question.
func IsRestrictedAudience(value string) bool {
	trimmed := strings.TrimSpace(value)
	for _, restricted := range restrictedAudienceValues {
		if strings.EqualFold(trimmed, restricted) {
			return true
		}
	}
	return false
}
