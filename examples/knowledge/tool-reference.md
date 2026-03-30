---
tags: [devops, forge, ha]
---
# Tool Reference

This knowledge article loads when devops, forge, or ha capability tags are active. It provides tool-specific notes and workflow patterns.

## Path prefixes and content resolution

Tool arguments containing path prefixes are automatically resolved before the handler runs. Two resolution modes:

**Path resolution** — prefix expands to an absolute filesystem path:
- `kb:devops/thane-ops.md` → `/path/to/knowledge/devops/thane-ops.md`
- `scratchpad:dev-status.md` → `/path/to/scratchpad/dev-status.md`
- `temp:LABEL` → `/path/to/.tmp/label-file` (from `create_temp_file`)

**Content resolution** — for tools that read content, the prefix resolves AND the file is read inline. Passing `kb:file.md` as a tool argument gives the handler the file's content, not the path.

Available prefixes depend on the `paths:` config. Common ones:
- `kb:` — knowledge base directory
- `scratchpad:` — inter-runtime communication / working notes
- `temp:` — ephemeral files created by `create_temp_file`
- `core:` — workspace root

### Temp files for large content

Use `create_temp_file` when composing large content for forge tools or delegates:

```
1. create_temp_file(label: "issue-body", content: "## Summary\n...")
2. forge_issue_update(repo: "thane-ai-agent", number: 93, body: "temp:issue-body")
   → body is resolved to full file content before the API call
```

```
1. create_temp_file(label: "pr-review-context", content: "<diff and notes>")
2. thane_delegate(task: "Review PR #595. Context: temp:pr-review-context", tags: ["forge"])
   → delegate sees the resolved content, not the label
```

**When to use temp files:**
- Issue/PR bodies longer than ~500 chars
- Delegate tasks that need structured context (diffs, logs, multi-file content)
- Any content you'd compose in multiple steps before submitting

**When NOT to use temp files:**
- Short messages (comments, titles, labels)
- Tool arguments that are already concise

## Forge (GitHub/Gitea)

**forge_issue_update body REPLACES** the entire description — not a patch. Use `create_temp_file` to compose the full body, then pass `temp:LABEL` as the body parameter. Use `forge_issue_comment` for additive updates.

**Default owner**: First configured account owner. Pass just the repo name (e.g., `"my-repo"`) or full `"owner/repo"`.

**PR get is comprehensive**: `forge_pr_get` includes inline review summary and check status — no need to follow up with `forge_pr_reviews` or `forge_pr_checks` unless you need per-review detail or per-check URLs.

**All forge responses are JSON**: Issue/PR get returns structured JSON with a prose body after a `---` separator. List and action responses are JSON objects. Parse the structured fields; read the body as markdown.

## Home Assistant

**Device control pattern**: `find_entity` → `call_service` → `get_state` (verify). Never trust `call_service` success alone — it doesn't validate entity names and silently no-ops on bad IDs.

**Discovery**: Use `find_entity` for name/keyword search. `list_entities` defaults to 20 entities per page but high limits produce very large outputs — prefer `find_entity` when you don't know the domain/entity ID. `ha_get_overview` is ~43K chars — use sparingly.

## Signal

Use `signal_send_message` and `signal_send_reaction`. Do NOT use `notify.signal_messenger` via HA — it doesn't exist.

## Media

`media_transcript(url, language="en", focus, detail)` supports YouTube, Vimeo, and podcasts. Detail levels: `full` (raw transcript), `summary` (2-3K chars), `brief` (~500 chars).
