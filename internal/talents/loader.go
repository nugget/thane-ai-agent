// Package talents loads and manages behavioral guidance documents.
package talents

import (
	"encoding/json"
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

// Talent represents a parsed talent file with optional tag metadata.
type Talent struct {
	Name    string   // Filename without .md extension
	Tags    []string // Tags from YAML frontmatter (nil = untagged)
	Content string   // Markdown content (frontmatter stripped)
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

// LoadAll reads all talent files and parses their frontmatter. Returns
// parsed Talent structs with tags extracted. Use FilterByTags to select
// talents matching active capability tags.
func (l *Loader) LoadAll() ([]Talent, error) {
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

	var talents []Talent
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(l.dir, f))
		if err != nil {
			return nil, fmt.Errorf("read talent %s: %w", f, err)
		}

		name := strings.TrimSuffix(f, ".md")
		tags, content := ParseFrontmatter(string(data))
		talents = append(talents, Talent{
			Name:    name,
			Tags:    tags,
			Content: content,
		})
	}

	return talents, nil
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
type ManifestEntry struct {
	Tag          string
	Description  string
	Tools        []string
	AlwaysActive bool
	KBArticles   int  // tagged KB articles auto-loaded when active
	LiveContext  bool // has a registered TagContextProvider
	AdHoc        bool // discovered from KB/talents, not in config
}

// capabilityJSON is the JSON structure for a single capability tag.
type capabilityJSON struct {
	Status      string      `json:"status"`
	Description string      `json:"description"`
	ToolCount   int         `json:"tools,omitempty"`
	Context     *ctxSummary `json:"context,omitempty"`
}

// ctxSummary describes context sources for a capability.
type ctxSummary struct {
	KBArticles int  `json:"kb_articles,omitempty"`
	Live       bool `json:"live,omitempty"`
}

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

	var sb strings.Builder
	sb.WriteString("### Available Capabilities\n\n")
	sb.WriteString("Activate with `activate_capability(tag: \"name\")`, or `delegate(task, tags: [\"name\"])` for one-off tasks. ")
	sb.WriteString("Deactivate with `deactivate_capability` when done. Ad-hoc tags work too — any tagged KB articles or talents will load.\n\n")

	// Sort entries by tag name for deterministic JSON output.
	// Input may come from map iteration (nondeterministic order).
	sorted := make([]ManifestEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Tag < sorted[j].Tag
	})

	// Build JSON capabilities map. Ad-hoc tags (discovered from
	// KB/talents but not in config) get status "discoverable" with
	// their context metadata preserved.
	caps := make(map[string]capabilityJSON, len(sorted))

	for _, e := range sorted {
		status := "available"
		switch {
		case e.AdHoc:
			status = "discoverable"
		case e.AlwaysActive:
			status = "always_active"
		}

		c := capabilityJSON{
			Status:      status,
			Description: e.Description,
			ToolCount:   len(e.Tools),
		}

		// Only include context summary if there are sources.
		if e.KBArticles > 0 || e.LiveContext {
			c.Context = &ctxSummary{
				KBArticles: e.KBArticles,
				Live:       e.LiveContext,
			}
		}

		caps[e.Tag] = c
	}

	wrapper := struct {
		Capabilities map[string]capabilityJSON `json:"capabilities"`
	}{
		Capabilities: caps,
	}

	data, err := json.Marshal(wrapper)
	if err != nil {
		// Emit valid JSON even on marshal failure.
		sb.WriteString(`{"error":"manifest marshal failed"}`)
	} else {
		sb.Write(data)
	}

	return &Talent{
		Name:    "_capability_manifest",
		Tags:    nil, // Untagged — always loads
		Content: sb.String(),
	}
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
