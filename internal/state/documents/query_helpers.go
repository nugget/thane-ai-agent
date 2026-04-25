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
