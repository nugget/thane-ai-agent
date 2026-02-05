// Package talents loads and manages behavioral guidance documents.
package talents

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Loader handles talent file loading.
type Loader struct {
	dir string
}

// NewLoader creates a talent loader for the given directory.
func NewLoader(dir string) *Loader {
	return &Loader{dir: dir}
}

// Load reads all .md files from the talents directory and returns
// their combined content, suitable for injection into system prompts.
func (l *Loader) Load() (string, error) {
	if l.dir == "" {
		return "", nil
	}

	entries, err := os.ReadDir(l.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // No talents dir is fine
		}
		return "", fmt.Errorf("read talents dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			files = append(files, e.Name())
		}
	}

	// Sort for deterministic ordering
	sort.Strings(files)

	var parts []string
	for _, f := range files {
		content, err := os.ReadFile(filepath.Join(l.dir, f))
		if err != nil {
			return "", fmt.Errorf("read talent %s: %w", f, err)
		}
		parts = append(parts, string(content))
	}

	if len(parts) == 0 {
		return "", nil
	}

	return strings.Join(parts, "\n\n---\n\n"), nil
}

// List returns the names of available talent files.
func (l *Loader) List() ([]string, error) {
	if l.dir == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(l.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			name := strings.TrimSuffix(e.Name(), ".md")
			names = append(names, name)
		}
	}

	sort.Strings(names)
	return names, nil
}
