package openclaw

import (
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/config"
)

func TestBuildSystemPrompt_NilConfig(t *testing.T) {
	_, err := BuildSystemPrompt(nil, false)
	if err == nil {
		t.Error("expected error for nil config")
	}
}

func TestBuildSystemPrompt_MainSession(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "AGENTS.md", "# Agent Rules\nBe helpful.")
	writeFile(t, dir, "SOUL.md", "# Soul\nYou are witty and kind.")
	writeFile(t, dir, "USER.md", "# User\nPrefers concise answers.")

	cfg := &config.OpenClawConfig{
		WorkspacePath: dir,
		MaxFileChars:  20000,
	}

	prompt, err := BuildSystemPrompt(cfg, false)
	if err != nil {
		t.Fatal(err)
	}

	// Identity line.
	if !strings.Contains(prompt, "personal assistant running inside OpenClaw") {
		t.Error("missing identity line")
	}

	// Skills section present (even if no skills found).
	if !strings.Contains(prompt, "## Memory Recall") {
		t.Error("missing Memory Recall section")
	}

	// Project Context with files.
	if !strings.Contains(prompt, "# Project Context") {
		t.Error("missing Project Context section")
	}
	if !strings.Contains(prompt, "## AGENTS.md") {
		t.Error("missing AGENTS.md in context")
	}
	if !strings.Contains(prompt, "## SOUL.md") {
		t.Error("missing SOUL.md in context")
	}
	if !strings.Contains(prompt, "embody its persona and tone") {
		t.Error("missing SOUL.md persona instruction")
	}

	// Heartbeat section.
	if !strings.Contains(prompt, "## Heartbeats") {
		t.Error("missing Heartbeats section")
	}
	if !strings.Contains(prompt, "HEARTBEAT_OK") {
		t.Error("missing HEARTBEAT_OK instruction")
	}
}

func TestBuildSystemPrompt_Subagent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "AGENTS.md", "# Rules")
	writeFile(t, dir, "TOOLS.md", "# Tool notes")
	writeFile(t, dir, "SOUL.md", "# Soul")
	writeFile(t, dir, "MEMORY.md", "# Memory")

	cfg := &config.OpenClawConfig{
		WorkspacePath: dir,
		MaxFileChars:  20000,
	}

	prompt, err := BuildSystemPrompt(cfg, true)
	if err != nil {
		t.Fatal(err)
	}

	// Subagent should have AGENTS.md and TOOLS.md.
	if !strings.Contains(prompt, "## AGENTS.md") {
		t.Error("subagent should include AGENTS.md")
	}
	if !strings.Contains(prompt, "## TOOLS.md") {
		t.Error("subagent should include TOOLS.md")
	}

	// Subagent should NOT have SOUL.md, MEMORY.md, Skills, Memory Recall, Heartbeats.
	if strings.Contains(prompt, "## SOUL.md") {
		t.Error("subagent should not include SOUL.md")
	}
	if strings.Contains(prompt, "## MEMORY.md") {
		t.Error("subagent should not include MEMORY.md")
	}
	if strings.Contains(prompt, "## Skills (mandatory)") {
		t.Error("subagent should not include Skills section")
	}
	if strings.Contains(prompt, "## Memory Recall") {
		t.Error("subagent should not include Memory Recall section")
	}
	if strings.Contains(prompt, "## Heartbeats") {
		t.Error("subagent should not include Heartbeats section")
	}
}

func TestBuildSystemPrompt_WithSkills(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "AGENTS.md", "# Rules")

	// Create a skill.
	skillsDir := dir + "/skills"
	makeSkill(t, skillsDir, "greet", `---
name: greet
description: Greet the user warmly
---
# Greeting Skill
`)

	cfg := &config.OpenClawConfig{
		WorkspacePath: dir,
		SkillsDirs:    []string{skillsDir},
		MaxFileChars:  20000,
	}

	prompt, err := BuildSystemPrompt(cfg, false)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(prompt, "## Skills (mandatory)") {
		t.Error("missing Skills section")
	}
	if !strings.Contains(prompt, "<available_skills>") {
		t.Error("missing <available_skills> block")
	}
	if !strings.Contains(prompt, "<name>greet</name>") {
		t.Error("missing greet skill in block")
	}
}

func TestBuildSystemPrompt_DatePresent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "AGENTS.md", "# Rules")

	cfg := &config.OpenClawConfig{
		WorkspacePath: dir,
		MaxFileChars:  20000,
	}

	prompt, err := BuildSystemPrompt(cfg, false)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(prompt, "## Current Date & Time") {
		t.Error("missing date/time section")
	}
}

func TestAssemblePrompt_NoFiles(t *testing.T) {
	prompt := assemblePrompt(nil, nil, false)

	// Should still have identity and structural sections.
	if !strings.Contains(prompt, "personal assistant") {
		t.Error("missing identity line with no files")
	}
	// No Project Context when no files loaded.
	if strings.Contains(prompt, "# Project Context") {
		t.Error("should not have Project Context with no files")
	}
}
