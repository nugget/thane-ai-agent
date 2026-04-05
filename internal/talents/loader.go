// Package talents loads and manages behavioral guidance documents.
package talents

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/toolcatalog"
)

// Loader handles talent file loading.
type Loader struct {
	dir string
}

// NewLoader creates a talent loader for the given directory.
func NewLoader(dir string) *Loader {
	return &Loader{dir: dir}
}

// Talent represents a parsed talent file with optional tag metadata.
type Talent struct {
	Name    string   // Filename without .md extension
	Tags    []string // Tags from YAML frontmatter (nil = untagged)
	Content string   // Markdown content (frontmatter stripped)
}

// listFiles returns a sorted slice of .md filenames in l.dir.
// Returns nil, nil when dir is unset or does not exist.
func (l *Loader) listFiles() ([]string, error) {
	if l.dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read talents dir: %w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files, nil
}

// Talents reads all .md files from the talents directory, parses their
// YAML frontmatter, and returns one Talent per file. Tags are extracted
// from frontmatter; Content has the frontmatter stripped. Use
// FilterByTags to select the subset matching active capability tags.
func (l *Loader) Talents() ([]Talent, error) {
	files, err := l.listFiles()
	if err != nil {
		return nil, err
	}
	var ts []Talent
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(l.dir, f))
		if err != nil {
			return nil, fmt.Errorf("read talent %s: %w", f, err)
		}
		name := strings.TrimSuffix(f, ".md")
		tags, content := ParseFrontmatter(string(data))
		ts = append(ts, Talent{Name: name, Tags: tags, Content: content})
	}
	return ts, nil
}

// FilterByTags returns the combined content of talents matching the
// given active tags. Untagged talents are always included (they have
// no tag restrictions). If activeTags is nil, all talents are included.
func FilterByTags(talents []Talent, activeTags map[string]bool) string {
	var parts []string
	for _, t := range talents {
		if shouldIncludeTalent(t, activeTags) {
			parts = append(parts, t.Content)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// shouldIncludeTalent returns true if the talent should be included
// given the active tag set. Untagged talents are always included.
// Tagged talents are included when any of their tags is active.
func shouldIncludeTalent(t Talent, activeTags map[string]bool) bool {
	if len(t.Tags) == 0 {
		return true // Untagged talents always load
	}
	if activeTags == nil {
		return true // No tag filtering active
	}
	for _, tag := range t.Tags {
		if activeTags[tag] {
			return true
		}
	}
	return false
}

// ParseFrontmatter extracts tags from YAML frontmatter delimited by
// "---" lines. Returns (tags, content) where content has the
// frontmatter stripped. If no frontmatter is found, returns (nil, raw).
//
// Supported frontmatter format:
//
//	---
//	tags: [ha, physical]
//	---
func ParseFrontmatter(raw string) ([]string, string) {
	if !strings.HasPrefix(raw, "---") {
		return nil, raw
	}

	// Find the closing "---" delimiter.
	rest := raw[3:]
	rest = strings.TrimLeft(rest, " \t")
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	} else if len(rest) > 1 && rest[0] == '\r' && rest[1] == '\n' {
		rest = rest[2:]
	} else {
		return nil, raw // No newline after opening ---
	}

	closeIdx := strings.Index(rest, "\n---")
	if closeIdx < 0 {
		return nil, raw // No closing ---
	}

	frontmatter := rest[:closeIdx]
	content := rest[closeIdx+4:] // Skip "\n---"
	content = strings.TrimLeft(content, "\r\n")

	tags := parseTagsLine(frontmatter)
	return tags, content
}

// parseTagsLine extracts tags from a "tags: [a, b, c]" line within
// frontmatter. Returns nil if no tags line is found.
func parseTagsLine(frontmatter string) []string {
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "tags:") {
			continue
		}
		value := strings.TrimPrefix(line, "tags:")
		value = strings.TrimSpace(value)

		// Handle [a, b, c] format.
		value = strings.TrimPrefix(value, "[")
		value = strings.TrimSuffix(value, "]")

		var tags []string
		for _, part := range strings.Split(value, ",") {
			tag := strings.TrimSpace(part)
			if tag != "" {
				tags = append(tags, tag)
			}
		}
		return tags
	}
	return nil
}

// ManifestEntry describes a capability tag for the auto-generated manifest.
type ManifestEntry = toolcatalog.CapabilitySurface

// GenerateManifest creates a Talent containing the capability manifest
// as compact JSON. Tool names are omitted — the model already has tool
// definitions in its schema. The manifest provides tag descriptions,
// tool counts, and context source metadata.
//
// The generated talent has no tags (always loads). Returns nil when
// entries is empty.
func GenerateManifest(entries []ManifestEntry) *Talent {
	if len(entries) == 0 {
		return nil
	}

	return &Talent{
		Name:    "_capability_manifest",
		Tags:    nil, // Untagged — always loads
		Content: toolcatalog.RenderCapabilityManifestMarkdown(entries),
	}
}
