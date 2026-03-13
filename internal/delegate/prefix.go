package delegate

import (
	"encoding/json"
	"fmt"
	"os"
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

// expandPathPrefixes replaces known prefix names at the start of path
// arguments in argsJSON. Most file tools use a single "path" key;
// file_stat uses a comma-separated "paths" key instead. Absolute paths
// and ~/... paths are left untouched. Unknown prefixes are left as-is.
//
// Prefixes are sorted by descending length so a longer prefix is matched
// before a shorter one that shares the same start (same pattern as
// [tools.TempFileStore.ExpandLabels]).
func expandPathPrefixes(toolName, argsJSON string, prefixes map[string]string) string {
	if len(prefixes) == 0 || argsJSON == "" {
		return argsJSON
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return argsJSON
	}

	// file_stat uses a comma-separated "paths" key.
	if toolName == "file_stat" {
		return expandStatPaths(args, argsJSON, prefixes)
	}

	pathVal, ok := args["path"].(string)
	if !ok || pathVal == "" {
		return argsJSON
	}

	expanded := expandSinglePath(pathVal, prefixes)
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

// expandStatPaths handles the comma-separated "paths" argument used by
// file_stat. Each entry is trimmed, expanded independently, and
// reassembled.
func expandStatPaths(args map[string]any, original string, prefixes map[string]string) string {
	pathsVal, ok := args["paths"].(string)
	if !ok || pathsVal == "" {
		return original
	}

	parts := strings.Split(pathsVal, ",")
	changed := false
	for i, p := range parts {
		p = strings.TrimSpace(p)
		expanded := expandSinglePath(p, prefixes)
		if expanded != p {
			changed = true
		}
		parts[i] = expanded
	}

	if !changed {
		return original
	}

	args["paths"] = strings.Join(parts, ",")
	out, err := json.Marshal(args)
	if err != nil {
		return original
	}
	return string(out)
}

// expandSinglePath expands a prefix in a single path string. Absolute
// and home-relative paths are returned unchanged.
func expandSinglePath(path string, prefixes map[string]string) string {
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "~/") {
		return path
	}
	return expandPrefix(path, prefixes)
}

// expandPrefix matches a prefix name at the start of path and replaces
// it with the corresponding full directory. Trailing slashes on prefix
// values are normalized so the result never contains "//". Returns the
// original path unchanged if no prefix matches.
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
		dir := strings.TrimRight(prefixes[name], "/")
		// Match "prefix/rest" or exact "prefix".
		if path == name {
			return dir
		}
		if strings.HasPrefix(path, name+"/") {
			return dir + path[len(name):]
		}
	}
	return path
}

// maxPrefixEntries caps the number of directory entries listed per
// prefix to avoid blowing up the system prompt.
const maxPrefixEntries = 50

// formatPrefixPrompt returns a system prompt block documenting the
// available path prefixes and a shallow listing of each directory's
// contents. Returns an empty string if no prefixes are defined.
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
		dir := strings.TrimRight(prefixes[name], "/")
		sb.WriteString(fmt.Sprintf("  %s/ → %s/\n", name, dir))
	}
	sb.WriteString("Use these prefixes at the start of file tool paths instead of full paths.\n")

	// Append a shallow directory listing for each prefix so the
	// delegate can reference files immediately without calling
	// file_list first.
	for _, name := range names {
		entries := listPrefixDir(prefixes[name])
		if len(entries) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("\n%s/ contents:\n", name))
		for _, e := range entries {
			sb.WriteString(fmt.Sprintf("  %s\n", e))
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

// listPrefixDir returns a shallow listing of the directory at path.
// Directories get a trailing "/". The result is capped at
// [maxPrefixEntries]; when truncated a summary line is appended.
// Returns nil if the path cannot be read.
func listPrefixDir(path string) []string {
	path = expandHome(path)
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}

	truncated := false
	if len(entries) > maxPrefixEntries {
		truncated = true
		entries = entries[:maxPrefixEntries]
	}

	result := make([]string, 0, len(entries)+1)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		result = append(result, name)
	}

	if truncated {
		result = append(result, fmt.Sprintf("... (list truncated at %d entries)", maxPrefixEntries))
	}

	return result
}

// expandHome replaces a leading "~/" with the user's home directory.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return home + path[1:]
}
