---
tags: [devops, forge, ha]
---
# Tool Reference

This knowledge article loads when devops, forge, or ha capability tags are active. It provides tool-specific notes that aren't covered by the tool definitions themselves.

## Forge (GitHub/Gitea)

**Issues:** `forge_issue_update` body REPLACES the entire description — not a patch. Use `forge_issue_comment` for additive updates.

**PRs:** `forge_pr_diff` returns up to 2000 lines by default. `forge_pr_merge` defaults to squash merge. `forge_pr_review` accepts APPROVE, COMMENT, or REQUEST_CHANGES.

**Default owner:** First configured account owner. Pass just the repo name (e.g., `"my-repo"`) or full `"owner/repo"`.

## Home Assistant

**Device control pattern:** `find_entity` → `call_service` → `get_state` (verify). Never trust `call_service` success alone — it doesn't validate entity names and silently no-ops on bad IDs.

**Discovery:** Use `find_entity` for name/keyword search. `list_entities` returns 10K-83K chars — never for discovery. `ha_get_overview` is ~43K chars — use sparingly.

## Signal

Use `signal_send_message` and `signal_send_reaction`. Do NOT use `notify.signal_messenger` via HA — it doesn't exist.

## Media

`media_transcript(url, language="en", focus, detail)` supports YouTube, Vimeo, and podcasts. Detail levels: `full` (raw transcript), `summary` (2-3K chars), `brief` (~500 chars).
