---
tags: [devops, forge, ha]
---
# Tool Reference

This knowledge article loads when devops, forge, or ha capability tags are active. It provides tool-specific notes and workflow patterns.

## Temp files for large content

Use `create_temp_file` when passing structured content to delegates or forge tools. The `temp:LABEL` reference is automatically resolved to file content before the tool handler runs.

**Pattern: Writing issue/PR descriptions**
```
1. create_temp_file(label: "issue-body", content: "## Summary\n...")
2. forge_issue_update(repo: "thane-ai-agent", number: 93, body: "temp:issue-body")
   → body is resolved to full file content before the API call
```

**Pattern: Delegating with context**
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
