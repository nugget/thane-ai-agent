// Package paths provides a shared prefix resolver for named directory
// paths. Components that need to resolve prefixed paths (kb:,
// scratchpad:, etc.) use a single [Resolver] instance built from
// configuration at startup.
package paths

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Resolver maps named prefixes to absolute directory paths. It is
// nil-safe: calling [Resolver.Resolve] on a nil *Resolver returns the
// input path unchanged, matching the nil-safe pattern used by the
// event bus.
type Resolver struct {
	prefixes map[string]string // "kb:" -> "/abs/path/to/vault"
	sorted   []string          // prefixes sorted by descending length
}

// New creates a Resolver from a prefix-to-directory map. Keys are
// prefix names without the trailing colon (e.g., "kb", not "kb:").
// Home directory tildes (~) in values are expanded at construction
// time. Returns nil if the map is empty or nil.
func New(prefixes map[string]string) *Resolver {
	if len(prefixes) == 0 {
		return nil
	}
	m := make(map[string]string, len(prefixes))
	sorted := make([]string, 0, len(prefixes))
	for name, dir := range prefixes {
		key := name
		if !strings.HasSuffix(key, ":") {
			key += ":"
		}
		m[key] = expandHome(dir)
		sorted = append(sorted, key)
	}
	// Sort by descending length so longer prefixes match first.
	// Prevents "kb:" from stealing matches intended for "kbase:".
	sort.Slice(sorted, func(i, j int) bool {
		return len(sorted[i]) > len(sorted[j])
	})
	return &Resolver{prefixes: m, sorted: sorted}
}

// Resolve expands a prefixed path to an absolute path. If no
// registered prefix matches, the original path is returned unchanged.
// A bare prefix (e.g., "kb:" with no trailing path) returns the root
// directory for that prefix.
func (r *Resolver) Resolve(path string) (string, error) {
	if r == nil {
		return path, nil
	}
	for _, prefix := range r.sorted {
		if strings.HasPrefix(path, prefix) {
			rel := strings.TrimPrefix(path, prefix)
			base := r.prefixes[prefix]
			if rel == "" {
				return base, nil
			}
			return filepath.Join(base, rel), nil
		}
	}
	return path, nil
}

// HasPrefix reports whether the path starts with a registered prefix.
func (r *Resolver) HasPrefix(path string) bool {
	if r == nil {
		return false
	}
	for _, prefix := range r.sorted {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// Prefixes returns the registered prefix names sorted alphabetically,
// without trailing colons. Useful for documentation and help output.
func (r *Resolver) Prefixes() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.prefixes))
	for prefix := range r.prefixes {
		names = append(names, strings.TrimSuffix(prefix, ":"))
	}
	sort.Strings(names)
	return names
}

// expandHome replaces a leading ~ with the user's home directory.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		return filepath.Join(home, path[2:])
	}
	return path
}
