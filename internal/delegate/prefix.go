package delegate

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/awareness"
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

// dirEntry is a single file or directory in a prefix listing.
type dirEntry struct {
	Name    string `json:"name"`
	Type    string `json:"type"`           // "file" or "dir"
	ModTime string `json:"mod,omitempty"`  // delta like "-3247s"
	Size    int64  `json:"size,omitempty"` // bytes, files only
}

// prefixInfo describes a single path prefix and its contents.
type prefixInfo struct {
	Dir       string     `json:"dir"`
	Entries   []dirEntry `json:"entries,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
}

// formatPrefixPrompt returns a system prompt block documenting the
// available path prefixes and a shallow listing of each directory's
// contents as structured JSON. Returns an empty string if no prefixes
// are defined.
func formatPrefixPrompt(prefixes map[string]string, now time.Time) string {
	if len(prefixes) == 0 {
		return ""
	}

	// Sort keys for deterministic output.
	names := make([]string, 0, len(prefixes))
	for name := range prefixes {
		names = append(names, name)
	}
	sort.Strings(names)

	info := make(map[string]prefixInfo, len(names))
	for _, name := range names {
		dir := strings.TrimRight(prefixes[name], "/")
		pi := prefixInfo{Dir: dir}
		entries, truncated := listPrefixDir(prefixes[name], now)
		pi.Entries = entries
		pi.Truncated = truncated
		info[name] = pi
	}

	listing, err := json.Marshal(info)
	if err != nil {
		listing = []byte("{}")
	}

	var sb strings.Builder
	sb.WriteString("Path prefixes available (use these at the start of file tool paths):\n")
	sb.Write(listing)
	return sb.String()
}

// listPrefixDir returns a shallow listing of the directory at path
// with modification times expressed as deltas relative to now. The
// result is capped at [maxPrefixEntries]. Returns nil entries if the
// path cannot be read.
func listPrefixDir(path string, now time.Time) ([]dirEntry, bool) {
	path = expandHome(path)
	rawEntries, err := os.ReadDir(path)
	if err != nil {
		return nil, false
	}

	truncated := false
	if len(rawEntries) > maxPrefixEntries {
		truncated = true
		rawEntries = rawEntries[:maxPrefixEntries]
	}

	result := make([]dirEntry, 0, len(rawEntries))
	for _, e := range rawEntries {
		de := dirEntry{Name: e.Name()}
		if e.IsDir() {
			de.Type = "dir"
		} else {
			de.Type = "file"
		}
		if info, err := e.Info(); err == nil {
			de.ModTime = awareness.FormatDeltaOnly(info.ModTime(), now)
			if !e.IsDir() {
				de.Size = info.Size()
			}
		}
		result = append(result, de)
	}

	return result, truncated
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
