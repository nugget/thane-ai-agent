# OpenClaw Compatibility (`thane:openclaw`)

> **Context:** [Issue #256](https://github.com/nugget/thane-ai-agent/issues/256) — `thane:openclaw` faux Ollama model

## What Is This?

The `thane:openclaw` profile exposes an Ollama-compatible model that replicates [OpenClaw](https://openclaw.com/)'s workspace-aware agent behavior using Thane's existing agent loop. Anything that speaks the Ollama chat API can use it, including OpenClaw itself as a backend.

This lets you run an OpenClaw-style agent (workspace files, skill discovery, memory conventions) without depending on OpenClaw's infrastructure. The agent loop, tool execution, streaming, and compaction are all handled by Thane's native plumbing.

## Why?

OpenClaw's reliability has degraded. Rather than wait for upstream fixes, this profile gives you the same workspace-driven behavior through Thane's stable infrastructure. If OpenClaw stabilizes, you can switch back; if not, your workspace files and skills continue working as-is.

## Quick Start

1. Create the workspace directory:

```bash
mkdir -p ~/Thane/openclaw/skills
```

2. Add the `openclaw` section to your `config.yaml`:

```yaml
openclaw:
  workspace: "~/Thane/openclaw"
  skills_dirs:
    - "~/Thane/openclaw/skills"
  max_file_chars: 20000      # optional, this is the default
```

3. Create at minimum an `AGENTS.md` file:

```bash
cat > ~/Thane/openclaw/AGENTS.md << 'EOF'
# Agent Rules

You are a helpful personal assistant. Follow these workspace conventions...
EOF
```

4. Restart Thane. The profile appears in `/api/tags` and is ready to use:

```bash
curl http://localhost:11434/api/chat -d '{
  "model": "thane:openclaw",
  "messages": [{"role": "user", "content": "Hello"}]
}'
```

## Workspace Layout

The workspace directory contains the files that define your agent's personality, knowledge, and behavior. Files are injected into the system prompt in a fixed order matching OpenClaw v2026.2.9.

```
~/Thane/openclaw/
  AGENTS.md          # Workspace conventions, safety rules, behavioral guidelines
  SOUL.md            # Persona and tone — who the agent "is"
  TOOLS.md           # Notes about available tools and how to use them
  IDENTITY.md        # Self-description and capabilities
  USER.md            # Context about the user (preferences, household, etc.)
  HEARTBEAT.md       # Instructions for periodic autonomous checks
  BOOTSTRAP.md       # Optional startup instructions (skipped if missing)
  MEMORY.md          # Curated long-term memory
  memory/            # Daily append logs (YYYY-MM-DD.md)
  skills/            # Skill directories (each containing SKILL.md)
```

### File Behavior

| File | Required | Notes |
|------|:---:|-------|
| `AGENTS.md` | Yes | Loaded for both main sessions and subagents |
| `SOUL.md` | Yes | Main session only. Triggers persona instructions in prompt. |
| `TOOLS.md` | Yes | Loaded for both main sessions and subagents |
| `IDENTITY.md` | Yes | Main session only |
| `USER.md` | Yes | Main session only |
| `HEARTBEAT.md` | Yes | Main session only |
| `BOOTSTRAP.md` | No | Silently skipped if missing |
| `MEMORY.md` | No | Loaded only if it exists on disk. Supports `memory.md` fallback. |

"Required" files that are missing will be included in the prompt with a `[MISSING]` marker, reminding the agent to create them. This is intentional — it bootstraps a new workspace by making the agent aware of what it should have.

Files exceeding `max_file_chars` (default 20,000) are truncated using a 70/20 head/tail strategy: 70% from the start of the file, a truncation marker, then 20% from the end.

## Skills

Skills are directories inside `skills_dirs` that contain a `SKILL.md` file with YAML frontmatter:

```
~/Thane/openclaw/skills/
  healthcheck/
    SKILL.md
  summarize/
    SKILL.md
    references/
```

Each `SKILL.md` must have a `description` in its frontmatter to be discoverable:

```markdown
---
name: healthcheck
description: Run a security and health audit of the workspace
---

# Healthcheck

When this skill is activated, perform the following steps...
```

The system prompt includes an `<available_skills>` block listing all discovered skills. The agent is instructed to read the appropriate SKILL.md on demand before acting — skills are not loaded upfront.

If the same skill name appears in multiple `skills_dirs`, the first directory wins (priority ordering).

## Memory Conventions

The `thane:openclaw` profile follows OpenClaw's memory model:

- **`MEMORY.md`** — Curated long-term memory. The agent reads this on every turn and is instructed to consult it before answering questions about prior work, preferences, or decisions.
- **`memory/YYYY-MM-DD.md`** — Daily append logs. The agent writes here when it learns something worth remembering.

The agent accesses these through Thane's file tools. The system prompt includes memory recall instructions that direct the agent to check workspace files before answering memory-dependent questions.

### Pre-Compaction Memory Flush

When a conversation's token count approaches the compaction threshold, the profile triggers a lightweight flush turn before compaction occurs. This gives the agent a chance to write important context to `memory/YYYY-MM-DD.md` before older messages are summarized away.

This matches OpenClaw's pre-compaction flush behavior and prevents memory loss during long conversations.

## Configuration Reference

```yaml
openclaw:
  # Root directory for workspace files.
  # Supports ~ expansion.
  # Default: ~/Thane/openclaw
  workspace: "~/Thane/openclaw"

  # Directories to scan for skill subdirectories.
  # Searched in order; first match wins for duplicate names.
  # Default: [~/Thane/openclaw/skills]
  skills_dirs:
    - "~/Thane/openclaw/skills"
    - "~/shared-skills"          # optional additional directories

  # Maximum characters per injected workspace file.
  # Files exceeding this are truncated (70% head / 20% tail).
  # Default: 20000
  max_file_chars: 20000
```

Setting `openclaw: null` or omitting the section entirely disables the profile. It will not appear in `/api/tags`.

## How It Works

The profile uses a **request-level prompt override**. When a request arrives for `thane:openclaw`:

1. The Ollama handler calls `openclaw.BuildSystemPrompt()` to assemble an OC-style system prompt from workspace files and skills
2. The assembled prompt is set on `Request.SystemPrompt`, which overrides the normal system prompt builder
3. The request is routed to a premium-tier model (same selection as `thane:premium`)
4. Thane's standard agent loop handles everything else: tool execution, streaming, compaction, and response generation

No separate loop, tool registry, or LLM client is involved. The only difference from other profiles is the system prompt content.

### Subagent Filtering

When the agent delegates to a subagent, only `AGENTS.md` and `TOOLS.md` are injected (matching OpenClaw's `SUBAGENT_BOOTSTRAP_ALLOWLIST`). Persona, memory, and heartbeat files are suppressed to keep subagent context lean.

## Compatibility Notes

- **OpenClaw v2026.2.9** — The workspace file order, truncation strategy, skill discovery format, and memory conventions match this version.
- **Case-insensitive filesystems** — On macOS (HFS+/APFS), both `MEMORY.md` and `memory.md` resolve to the same file. The loader handles this correctly and will not load the file twice.
- **Ollama API** — The profile is accessible from any Ollama-compatible client. It appears in the model list at `GET /api/tags`.
