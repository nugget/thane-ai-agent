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
	Kind    string   // Optional frontmatter kind (for example entry_point)
	Content string   // Markdown content (frontmatter stripped)
}

// Frontmatter captures the subset of markdown metadata Thane currently
// understands for talents and tagged KB articles.
type Frontmatter struct {
	Tags     []string
	Kind     string
	Teaser   string
	NextTags []string
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
		meta, content := ParseFrontmatterMetadata(string(data))
		ts = append(ts, Talent{Name: name, Tags: meta.Tags, Kind: meta.Kind, Content: content})
	}
	return ts, nil
}

// FilterByTags returns the combined content of talents matching the
// given active tags. Untagged talents are always included (they have
// no tag restrictions). If activeTags is nil, all talents are included.
func FilterByTags(talents []Talent, activeTags map[string]bool) string {
	alwaysOn, tagged := SplitByTags(talents, activeTags)
	switch {
	case alwaysOn == "":
		return tagged
	case tagged == "":
		return alwaysOn
	default:
		return alwaysOn + "\n\n---\n\n" + tagged
	}
}

func talentOrderKey(t Talent) int {
	switch {
	case len(t.Tags) == 0:
		return 0
	case strings.TrimSpace(t.Kind) == "entry_point":
		return 1
	default:
		return 2
	}
}

// SplitByTags partitions included talents into always-on and tagged
// groups, preserving the same ordering rules used by FilterByTags.
func SplitByTags(all []Talent, activeTags map[string]bool) (alwaysOn string, tagged string) {
	var alwaysIncluded []Talent
	var taggedIncluded []Talent
	for _, t := range all {
		if !shouldIncludeTalent(t, activeTags) {
			continue
		}
		if len(t.Tags) == 0 {
			alwaysIncluded = append(alwaysIncluded, t)
			continue
		}
		taggedIncluded = append(taggedIncluded, t)
	}
	sort.SliceStable(alwaysIncluded, func(i, j int) bool {
		return talentOrderKey(alwaysIncluded[i]) < talentOrderKey(alwaysIncluded[j])
	})
	sort.SliceStable(taggedIncluded, func(i, j int) bool {
		return talentOrderKey(taggedIncluded[i]) < talentOrderKey(taggedIncluded[j])
	})
	return renderTalents(alwaysIncluded), renderTalents(taggedIncluded)
}

func renderTalents(included []Talent) string {
	if len(included) == 0 {
		return ""
	}
	parts := make([]string, 0, len(included))
	for _, t := range included {
		parts = append(parts, t.Content)
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
	meta, content := ParseFrontmatterMetadata(raw)
	return meta.Tags, content
}

// ParseFrontmatterMetadata extracts the supported frontmatter fields and
// returns both the parsed metadata and the stripped body content. Unknown
// keys are ignored.
func ParseFrontmatterMetadata(raw string) (Frontmatter, string) {
	if !strings.HasPrefix(raw, "---") {
		return Frontmatter{}, raw
	}

	// Find the closing "---" delimiter.
	rest := raw[3:]
	rest = strings.TrimLeft(rest, " \t")
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	} else if len(rest) > 1 && rest[0] == '\r' && rest[1] == '\n' {
		rest = rest[2:]
	} else {
		return Frontmatter{}, raw // No newline after opening ---
	}

	closeIdx := strings.Index(rest, "\n---")
	if closeIdx < 0 {
		return Frontmatter{}, raw // No closing ---
	}

	frontmatter := rest[:closeIdx]
	content := rest[closeIdx+4:] // Skip "\n---"
	content = strings.TrimLeft(content, "\r\n")

	meta := parseFrontmatterLines(frontmatter)
	return meta, content
}

// parseFrontmatterLines extracts the currently supported metadata keys
// from frontmatter. Unknown keys are ignored.
func parseFrontmatterLines(frontmatter string) Frontmatter {
	var meta Frontmatter
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "tags:"):
			value := strings.TrimPrefix(line, "tags:")
			value = strings.TrimSpace(value)

			// Handle [a, b, c] format.
			value = strings.TrimPrefix(value, "[")
			value = strings.TrimSuffix(value, "]")

			var tags []string
			for _, part := range strings.Split(value, ",") {
				tag := strings.Trim(strings.TrimSpace(part), `"'`)
				if tag != "" {
					tags = append(tags, tag)
				}
			}
			meta.Tags = tags
		case strings.HasPrefix(line, "kind:"):
			value := strings.TrimSpace(strings.TrimPrefix(line, "kind:"))
			value = strings.Trim(value, `"'`)
			meta.Kind = value
		case strings.HasPrefix(line, "teaser:"):
			value := strings.TrimSpace(strings.TrimPrefix(line, "teaser:"))
			value = strings.Trim(value, `"'`)
			meta.Teaser = value
		case strings.HasPrefix(line, "next_tags:"):
			value := strings.TrimSpace(strings.TrimPrefix(line, "next_tags:"))
			value = strings.TrimPrefix(value, "[")
			value = strings.TrimSuffix(value, "]")
			var tags []string
			for _, part := range strings.Split(value, ",") {
				tag := strings.Trim(strings.TrimSpace(part), `"'`)
				if tag != "" {
					tags = append(tags, tag)
				}
			}
			meta.NextTags = tags
		default:
			continue
		}
	}
	return meta
}

// ManifestEntry describes a capability tag for the auto-generated manifest.
type ManifestEntry = toolcatalog.CapabilitySurface

// GenerateManifest creates a Talent containing the capability menu as
// compact JSON. Tool names are omitted — the model already has tool
// definitions in its schema. The menu provides root-tag descriptions,
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
