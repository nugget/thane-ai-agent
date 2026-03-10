package openclaw

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SkillEntry represents a discovered SKILL.md file.
type SkillEntry struct {
	// Name is the skill directory name (e.g., "healthcheck").
	Name string

	// Description is extracted from YAML frontmatter.
	Description string

	// Location is the absolute path to the SKILL.md file.
	Location string
}

// DiscoverSkills scans directories for SKILL.md files and returns
// entries sorted by name. Directories are searched in priority order;
// skills in earlier directories override same-named skills in later ones.
func DiscoverSkills(dirs []string) []SkillEntry {
	seen := make(map[string]bool)
	var entries []SkillEntry

	for _, dir := range dirs {
		dirEntries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, de := range dirEntries {
			if !de.IsDir() {
				continue
			}
			name := de.Name()
			if seen[name] {
				continue // higher-priority dir already provided this skill
			}

			skillPath := filepath.Join(dir, name, "SKILL.md")
			entry, ok := parseSkillFile(skillPath, name)
			if !ok {
				continue
			}

			seen[name] = true
			entries = append(entries, entry)
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries
}

// parseSkillFile reads a SKILL.md file and extracts name and description
// from YAML frontmatter. Returns ok=false if the file doesn't exist or
// has no description.
func parseSkillFile(path, dirName string) (SkillEntry, bool) {
	f, err := os.Open(path)
	if err != nil {
		return SkillEntry{}, false
	}
	defer f.Close()

	var (
		inFrontmatter bool
		name          string
		description   string
	)

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineNum++

		if lineNum == 1 && strings.TrimSpace(line) == "---" {
			inFrontmatter = true
			continue
		}
		if inFrontmatter && strings.TrimSpace(line) == "---" {
			break // end of frontmatter
		}
		if !inFrontmatter {
			break // no frontmatter
		}

		// Simple YAML key: value extraction (no nested structures).
		key, val, ok := parseYAMLLine(line)
		if !ok {
			continue
		}
		switch key {
		case "name":
			name = val
		case "description":
			description = val
		}
	}

	if name == "" {
		name = dirName
	}
	if description == "" {
		return SkillEntry{}, false // skills without descriptions are not discoverable
	}

	return SkillEntry{
		Name:        name,
		Description: description,
		Location:    path,
	}, true
}

// parseYAMLLine extracts a simple key: value pair from a YAML line.
func parseYAMLLine(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	// Strip surrounding quotes.
	value = strings.Trim(value, `"'`)
	if key == "" {
		return "", "", false
	}
	return key, value, true
}

// FormatSkillsBlock produces the <available_skills> XML block that the
// system prompt references for skill discovery.
func FormatSkillsBlock(skills []SkillEntry) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<available_skills>\n")
	for _, s := range skills {
		fmt.Fprintf(&sb, "<skill>\n  <name>%s</name>\n  <description>%s</description>\n  <location>%s</location>\n</skill>\n",
			xmlEscape(s.Name), xmlEscape(s.Description), xmlEscape(s.Location))
	}
	sb.WriteString("</available_skills>")
	return sb.String()
}

// xmlEscape performs minimal XML escaping for text content.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
