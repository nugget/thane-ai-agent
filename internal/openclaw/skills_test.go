package openclaw

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverSkills_BasicDiscovery(t *testing.T) {
	dir := t.TempDir()

	// Create two skill directories.
	makeSkill(t, dir, "healthcheck", `---
name: healthcheck
description: Run a security audit of the workspace
---
# Healthcheck Skill
`)
	makeSkill(t, dir, "summarize", `---
name: summarize
description: Summarize a document or conversation
---
# Summarize Skill
`)

	skills := DiscoverSkills([]string{dir})

	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2", len(skills))
	}
	// Sorted alphabetically.
	if skills[0].Name != "healthcheck" {
		t.Errorf("skills[0].Name = %q, want healthcheck", skills[0].Name)
	}
	if skills[1].Name != "summarize" {
		t.Errorf("skills[1].Name = %q, want summarize", skills[1].Name)
	}
	if skills[0].Description != "Run a security audit of the workspace" {
		t.Errorf("skills[0].Description = %q", skills[0].Description)
	}
}

func TestDiscoverSkills_PriorityOverride(t *testing.T) {
	high := t.TempDir()
	low := t.TempDir()

	makeSkill(t, high, "greet", `---
name: greet
description: High priority greeting
---
`)
	makeSkill(t, low, "greet", `---
name: greet
description: Low priority greeting
---
`)

	skills := DiscoverSkills([]string{high, low})

	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1 (deduped)", len(skills))
	}
	if skills[0].Description != "High priority greeting" {
		t.Errorf("expected high-priority skill, got description = %q", skills[0].Description)
	}
}

func TestDiscoverSkills_NoDescription(t *testing.T) {
	dir := t.TempDir()

	// Skill without description should be excluded.
	makeSkill(t, dir, "nodesc", `---
name: nodesc
---
# No description
`)

	skills := DiscoverSkills([]string{dir})

	if len(skills) != 0 {
		t.Errorf("skill without description should be excluded, got %d skills", len(skills))
	}
}

func TestDiscoverSkills_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()

	makeSkill(t, dir, "plain", "# Just a plain markdown file\nNo frontmatter here.")

	skills := DiscoverSkills([]string{dir})

	if len(skills) != 0 {
		t.Errorf("skill without frontmatter should be excluded, got %d", len(skills))
	}
}

func TestDiscoverSkills_MissingDirectory(t *testing.T) {
	skills := DiscoverSkills([]string{"/nonexistent/path"})

	if len(skills) != 0 {
		t.Errorf("missing directory should return no skills, got %d", len(skills))
	}
}

func TestDiscoverSkills_FallbackName(t *testing.T) {
	dir := t.TempDir()

	// Skill with description but no name — should use directory name.
	makeSkill(t, dir, "my-skill", `---
description: A skill without explicit name
---
`)

	skills := DiscoverSkills([]string{dir})

	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1", len(skills))
	}
	if skills[0].Name != "my-skill" {
		t.Errorf("Name = %q, want %q (dir name fallback)", skills[0].Name, "my-skill")
	}
}

func TestFormatSkillsBlock_Empty(t *testing.T) {
	got := FormatSkillsBlock(nil)
	if got != "" {
		t.Errorf("FormatSkillsBlock(nil) = %q, want empty", got)
	}
}

func TestFormatSkillsBlock_Format(t *testing.T) {
	skills := []SkillEntry{
		{Name: "test", Description: "A test skill", Location: "/path/to/SKILL.md"},
	}

	got := FormatSkillsBlock(skills)

	if !strings.Contains(got, "<available_skills>") {
		t.Error("should contain <available_skills> tag")
	}
	if !strings.Contains(got, "<name>test</name>") {
		t.Error("should contain skill name")
	}
	if !strings.Contains(got, "<description>A test skill</description>") {
		t.Error("should contain skill description")
	}
	if !strings.Contains(got, "</available_skills>") {
		t.Error("should contain closing tag")
	}
}

func TestFormatSkillsBlock_XmlEscape(t *testing.T) {
	skills := []SkillEntry{
		{Name: "test<>", Description: "A & B", Location: "/path"},
	}

	got := FormatSkillsBlock(skills)

	if strings.Contains(got, "test<>") {
		t.Error("should XML-escape angle brackets in name")
	}
	if !strings.Contains(got, "test&lt;&gt;") {
		t.Error("should escape < and > to &lt; and &gt;")
	}
	if !strings.Contains(got, "A &amp; B") {
		t.Error("should escape & to &amp;")
	}
}

// --- helpers ---

func makeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
