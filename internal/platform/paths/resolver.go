// Package paths provides a shared prefix resolver for named directory
// paths. Components that need to resolve prefixed paths (kb:,
// scratchpad:, etc.) use a single [Resolver] instance built from
// configuration at startup.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// RootKind identifies the policy shape carried by a named root.
type RootKind string

const (
	// RootKindDocument is a configured document corpus. Its richer indexing,
	// authoring, and provenance policy remains owned by the document store.
	RootKindDocument RootKind = "document"

	// RootKindRepository is a forge-maintained source checkout. Repository
	// roots are read-only to model-facing file tools and expose scoped git
	// history instead of document-signature semantics.
	RootKindRepository RootKind = "repository"
)

// Root is one named filesystem root known to the shared resolver.
//
// Owner is an opaque runtime owner used to make dynamic unregistration safe;
// it is not model-facing. Configured roots have no owner and live for the
// process lifetime. Repository subscriptions use their durable subscription
// ID so one subscription cannot unregister another subscription's root.
type Root struct {
	Name     string
	Path     string
	Kind     RootKind
	ReadOnly bool
	Owner    string
}

// Resolver maps named prefixes to absolute directory paths. It is
// nil-safe: calling [Resolver.Resolve] on a nil *Resolver returns the
// input path unchanged, matching the nil-safe pattern used by the
// event bus.
type Resolver struct {
	mu       sync.RWMutex
	roots    map[string]Root // canonical name (without :) -> root
	prefixes map[string]Root // "kb:" -> root
	sorted   []string        // prefixes sorted by descending length
}

// New creates a Resolver from a prefix-to-directory map. Keys are
// prefix names without the trailing colon (e.g., "kb", not "kb:").
// Home directory tildes (~) in values are expanded at construction
// time. Returns nil if the map is empty or nil.
func New(prefixes map[string]string) *Resolver {
	if len(prefixes) == 0 {
		return nil
	}
	r := &Resolver{
		roots:    make(map[string]Root, len(prefixes)),
		prefixes: make(map[string]Root, len(prefixes)),
	}
	for name, dir := range prefixes {
		root := Root{
			Name: canonicalName(name),
			Path: ExpandHome(dir),
			Kind: RootKindDocument,
		}
		r.registerLocked(root)
	}
	r.sortLocked()
	return r
}

// Register adds a dynamic named root. Register is concurrency-safe and
// refuses to replace an existing name with a different root.
func (r *Resolver) Register(root Root) error {
	_, err := r.RegisterCreated(root)
	return err
}

// RegisterCreated adds a dynamic named root and reports whether this call
// inserted it. An identical existing root is successful with created=false,
// allowing callers to roll back only state they own.
func (r *Resolver) RegisterCreated(root Root) (created bool, err error) {
	if r == nil {
		return false, fmt.Errorf("path resolver is not configured")
	}
	root.Name = canonicalName(root.Name)
	root.Path = ExpandHome(strings.TrimSpace(root.Path))
	if root.Name == "" {
		return false, fmt.Errorf("root name is required")
	}
	if root.Path == "" {
		return false, fmt.Errorf("root %q path is required", root.Name)
	}
	if root.Kind == "" {
		root.Kind = RootKindDocument
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.roots[root.Name]; ok {
		if existing == root {
			return false, nil
		}
		return false, fmt.Errorf("root %q is already registered", root.Name)
	}
	r.registerLocked(root)
	r.sortLocked()
	return true, nil
}

// Unregister removes a dynamic root only when owner matches the owner that
// registered it. Configured roots and roots owned by another subscription are
// left untouched.
func (r *Resolver) Unregister(name, owner string) bool {
	if r == nil || strings.TrimSpace(owner) == "" {
		return false
	}
	name = canonicalName(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	root, ok := r.roots[name]
	if !ok || root.Owner != owner {
		return false
	}
	delete(r.roots, name)
	delete(r.prefixes, name+":")
	r.sortLocked()
	return true
}

// Root returns one registered root by name, accepting an optional trailing
// colon. The returned value is a copy.
func (r *Resolver) Root(name string) (Root, bool) {
	if r == nil {
		return Root{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	root, ok := r.roots[canonicalName(name)]
	return root, ok
}

// RepositoryRoots returns repository roots sorted by name.
func (r *Resolver) RepositoryRoots() []Root {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	roots := make([]Root, 0)
	for _, root := range r.roots {
		if root.Kind == RootKindRepository {
			roots = append(roots, root)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Name < roots[j].Name })
	return roots
}

// Resolve expands a prefixed path to an absolute path. If no
// registered prefix matches, the original path is returned unchanged.
// A bare prefix (e.g., "kb:" with no trailing path) returns the root
// directory for that prefix.
func (r *Resolver) Resolve(path string) (string, error) {
	resolved, _, _ := r.ResolveRoot(path)
	return resolved, nil
}

// ResolveRoot expands a prefixed path and also reports the root that matched.
// The boolean is false for an ordinary filesystem path with no named prefix.
func (r *Resolver) ResolveRoot(path string) (string, Root, bool) {
	if r == nil {
		return path, Root{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, prefix := range r.sorted {
		if strings.HasPrefix(path, prefix) {
			rel := strings.TrimPrefix(path, prefix)
			root := r.prefixes[prefix]
			if rel == "" {
				return root.Path, root, true
			}
			return filepath.Join(root.Path, rel), root, true
		}
	}
	return path, Root{}, false
}

// HasPrefix reports whether the path starts with a registered prefix.
func (r *Resolver) HasPrefix(path string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
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
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.roots))
	for name := range r.roots {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RootForPath reports the most-specific registered root containing path.
func (r *Resolver) RootForPath(path string) (Root, bool) {
	if r == nil {
		return Root{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	candidate, err := normalizedAbsolutePath(path)
	if err != nil {
		return Root{}, false
	}
	var best Root
	bestLen := -1
	for _, root := range r.roots {
		normalizedRoot, err := normalizedAbsolutePath(root.Path)
		if err != nil {
			continue
		}
		if _, contained := lexicalRelative(normalizedRoot, candidate); !contained {
			continue
		}
		if len(normalizedRoot) < bestLen || (len(normalizedRoot) == bestLen && best.Name != "" && root.Name >= best.Name) {
			continue
		}
		best = root
		bestLen = len(normalizedRoot)
	}
	return best, bestLen >= 0
}

// ContainsPath reports whether candidate is root itself or a descendant of
// root. It uses filepath.Rel rather than string-prefix matching so sibling
// names such as /repo and /repo-old do not collapse into one boundary.
func ContainsPath(root, candidate string) bool {
	_, ok := RelativePath(root, candidate)
	return ok
}

// RelativePath returns candidate relative to root when candidate is root
// itself or a descendant. Both sides are normalized through existing symlink
// ancestors so macOS aliases such as /var and /private/var compare correctly.
func RelativePath(root, candidate string) (string, bool) {
	rootAbs, err := normalizedAbsolutePath(root)
	if err != nil {
		return "", false
	}
	candidateAbs, err := normalizedAbsolutePath(candidate)
	if err != nil {
		return "", false
	}
	return lexicalRelative(rootAbs, candidateAbs)
}

func normalizedAbsolutePath(path string) (string, error) {
	absPath, err := filepath.Abs(ExpandHome(path))
	if err != nil {
		return "", err
	}
	return resolveSymlinksBestEffort(absPath), nil
}

func lexicalRelative(root, candidate string) (string, bool) {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", false
	}
	if rel != "." && (rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return "", false
	}
	return rel, true
}

func resolveSymlinksBestEffort(path string) string {
	current := path
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func canonicalName(name string) string {
	return strings.TrimSuffix(strings.TrimSpace(name), ":")
}

func (r *Resolver) registerLocked(root Root) {
	root.Name = canonicalName(root.Name)
	if root.Kind == "" {
		root.Kind = RootKindDocument
	}
	r.roots[root.Name] = root
	r.prefixes[root.Name+":"] = root
}

func (r *Resolver) sortLocked() {
	r.sorted = r.sorted[:0]
	for prefix := range r.prefixes {
		r.sorted = append(r.sorted, prefix)
	}
	// Sort by descending length so longer prefixes match first.
	// Prevents "kb:" from stealing matches intended for "kbase:".
	sort.Slice(r.sorted, func(i, j int) bool {
		if len(r.sorted[i]) == len(r.sorted[j]) {
			return r.sorted[i] < r.sorted[j]
		}
		return len(r.sorted[i]) > len(r.sorted[j])
	})
}

// ExpandHome replaces a leading ~ with the user's home directory.
func ExpandHome(path string) string {
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
