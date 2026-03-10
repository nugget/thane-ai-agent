package openclaw

import (
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/paths"
)

// BuildSystemPrompt assembles an OpenClaw-style system prompt from
// workspace files and skills. The prompt structure matches OpenClaw
// v2026.2.9's buildAgentSystemPrompt().
//
// When subagent is true, only AGENTS.md and TOOLS.md are injected and
// skill/memory/heartbeat sections are suppressed.
func BuildSystemPrompt(cfg *config.OpenClawConfig, subagent bool) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("openclaw config is nil")
	}

	dir := paths.ExpandHome(cfg.WorkspacePath)
	maxChars := cfg.MaxFileChars
	if maxChars <= 0 {
		maxChars = 20000
	}

	// Load workspace files.
	files := LoadWorkspaceFiles(dir, subagent, maxChars)

	// Discover skills.
	var skillsDirs []string
	for _, d := range cfg.SkillsDirs {
		skillsDirs = append(skillsDirs, paths.ExpandHome(d))
	}
	skills := DiscoverSkills(skillsDirs)

	return assemblePrompt(files, skills, subagent), nil
}

// assemblePrompt builds the system prompt text from loaded components.
func assemblePrompt(files []WorkspaceFile, skills []SkillEntry, subagent bool) string {
	var sb strings.Builder

	// 1. Identity line.
	sb.WriteString("You are a personal assistant running inside OpenClaw.\n")
	sb.WriteString("Your workspace is your home directory. You have persistent memory through workspace files.\n\n")

	// 2. Skills section (main session only).
	if !subagent {
		skillsBlock := FormatSkillsBlock(skills)
		if skillsBlock != "" {
			sb.WriteString("## Skills (mandatory)\n\n")
			sb.WriteString("Before replying: scan <available_skills> <description> entries.\n")
			sb.WriteString("- If exactly one skill clearly applies: read its SKILL.md at <location> with the file read tool, then follow it.\n")
			sb.WriteString("- If multiple could apply: choose the most specific one, then read/follow it.\n")
			sb.WriteString("- If none clearly apply: do not read any SKILL.md.\n")
			sb.WriteString("Constraints: never read more than one skill up front; only read after selecting.\n\n")
			sb.WriteString(skillsBlock)
			sb.WriteString("\n\n")
		}
	}

	// 3. Memory recall instructions (main session only).
	if !subagent {
		sb.WriteString("## Memory Recall\n\n")
		sb.WriteString("Before answering anything about prior work, decisions, dates, people, preferences, or todos:\n")
		sb.WriteString("- Check MEMORY.md and memory/*.md files in your workspace for relevant context.\n")
		sb.WriteString("- Use workspace file tools to read only the needed sections.\n")
		sb.WriteString("- If you learn something worth remembering, write it to memory/YYYY-MM-DD.md (today's date).\n\n")
	}

	// 4. Date/time hint.
	now := time.Now()
	fmt.Fprintf(&sb, "## Current Date & Time\n\n%s\n\n", now.Format("Monday, January 2, 2006 3:04 PM MST"))

	// 5. Project Context — injected workspace files.
	var contextFiles []WorkspaceFile
	for _, f := range files {
		if f.Content != "" {
			contextFiles = append(contextFiles, f)
		}
	}

	if len(contextFiles) > 0 {
		sb.WriteString("# Project Context\n\n")
		sb.WriteString("The following project context files have been loaded:\n")

		// Check for SOUL.md presence (only trigger persona if it exists, not [MISSING]).
		hasSoul := false
		for _, f := range contextFiles {
			if f.Name == "SOUL.md" && !f.Missing {
				hasSoul = true
				break
			}
		}
		if hasSoul {
			sb.WriteString("If SOUL.md is present, embody its persona and tone in all responses.\n")
		}
		sb.WriteString("\n")

		for _, f := range contextFiles {
			fmt.Fprintf(&sb, "## %s\n\n%s\n\n", f.Name, f.Content)
		}
	}

	// 6. Heartbeat instructions (main session only).
	if !subagent {
		sb.WriteString("## Heartbeats\n\n")
		sb.WriteString("If you receive a heartbeat poll and there is nothing that needs attention, reply exactly:\n")
		sb.WriteString("HEARTBEAT_OK\n\n")
		sb.WriteString("Do not add any other text around HEARTBEAT_OK when nothing needs attention.\n")
		sb.WriteString("If something does need attention, reply with a clear, concise alert.\n")
	}

	return sb.String()
}
