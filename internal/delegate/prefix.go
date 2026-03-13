package delegate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// fileTools lists tool names whose "path" argument should have path
// prefixes expanded.
var fileTools = map[string]bool{
	"file_read":   true,
	"file_write":  true,
	"file_edit":   true,
	"file_list":   true,
	"file_search": true,
	"file_grep":   true,
	"file_stat":   true,
	"file_tree":   true,
}

// expandPathPrefixes replaces known prefix names at the start of the
// "path" argument in argsJSON. Only file tool arguments are expanded.
// Absolute paths and ~/... paths are left untouched. Unknown prefixes
// are left as-is.
//
// Prefixes are sorted by descending length so a longer prefix is matched
// before a shorter one that shares the same start (same pattern as
// [tools.TempFileStore.ExpandLabels]).
func expandPathPrefixes(argsJSON string, prefixes map[string]string) string {
	if len(prefixes) == 0 || argsJSON == "" {
		return argsJSON
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return argsJSON
	}

	pathVal, ok := args["path"].(string)
	if !ok || pathVal == "" {
		return argsJSON
	}

	// Don't expand absolute or home-relative paths.
	if strings.HasPrefix(pathVal, "/") || strings.HasPrefix(pathVal, "~/") {
		return argsJSON
	}

	expanded := expandPrefix(pathVal, prefixes)
	if expanded == pathVal {
		return argsJSON
	}

	args["path"] = expanded
	out, err := json.Marshal(args)
	if err != nil {
		return argsJSON
	}
	return string(out)
}

// expandPrefix matches a prefix name at the start of path and replaces
// it with the corresponding full directory. Returns the original path
// unchanged if no prefix matches.
func expandPrefix(path string, prefixes map[string]string) string {
	// Sort by descending length to match longest prefix first.
	sorted := make([]string, 0, len(prefixes))
	for name := range prefixes {
		sorted = append(sorted, name)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return len(sorted[i]) > len(sorted[j])
	})

	for _, name := range sorted {
		// Match "prefix/rest" or exact "prefix".
		if path == name {
			return prefixes[name]
		}
		if strings.HasPrefix(path, name+"/") {
			return prefixes[name] + path[len(name):]
		}
	}
	return path
}

// formatPrefixPrompt returns a system prompt block documenting the
// available path prefixes. Returns an empty string if no prefixes are
// defined.
func formatPrefixPrompt(prefixes map[string]string) string {
	if len(prefixes) == 0 {
		return ""
	}

	// Sort keys for deterministic output.
	names := make([]string, 0, len(prefixes))
	for name := range prefixes {
		names = append(names, name)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString("Path prefixes available:\n")
	for _, name := range names {
		sb.WriteString(fmt.Sprintf("  %s/ → %s/\n", name, strings.TrimRight(prefixes[name], "/")))
	}
	sb.WriteString("Use these prefixes at the start of file tool paths instead of full paths.")
	return sb.String()
}
