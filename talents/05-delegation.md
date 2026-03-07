# Delegation

Delegates are your hands, not your brain. They execute precisely what you describe — no more, no less. Every failure mode you'll encounter comes from assuming they'll figure something out on their own.

## The rules you'll want to skip (don't)

**Always end with an output instruction.** "Return the results as text." Without this, the delegate calls tools, produces nothing, and exhausts itself. This is 44% of all production failures.

**Tell them which tool to use.** The more specific, the fewer wasted iterations.

**Provide exact paths, entity IDs, and commands.** Delegates don't discover — they execute.

**One action per delegation.** Don't combine read + check + write. Three delegations.

**One entity type per delegation.** Multi-entity tasks waste iterations searching across unrelated domains.

**Include error recovery.** "If this fails, report the error and stop." Prevents retry loops.

**Tell them what NOT to do.** "Do NOT search the filesystem." "Do NOT try alternative approaches." Negative constraints are as important as positive instructions.

## Passing large content

Use `create_temp_file` to pass big text (issue bodies, config blocks) to delegates. Don't inline it — the quoting/escaping breaks.

```
create_temp_file(label="issue_body", content="# Full spec...")
```

Then in guidance: "Read temp:issue_body and use the contents to update issue #258."

Forge tools resolve `temp:LABEL` natively — pass `body="temp:issue_body"` directly.

Labels are semantic: `issue_body`, `review_comments`, `config_patch`. Automatic cleanup when the conversation ends.

## Failure patterns you'll recognize

| What happens | Why | Fix |
|---|---|---|
| Delegate produces no output | No output instruction | End with "Return [thing] as text." |
| Delegate spirals searching for files | You didn't give the exact path | Provide literal paths |
| Shell variables don't expand in file tools | `$(whoami)` doesn't work in file_read | Use literal paths |
| Delegate guesses instead of using tools | It thinks it knows | "Use [tool] — do not guess." |
| Delegate retries 15 variations | No error recovery instruction | "If this fails, report the error and stop." |
| Delegate checks one entity and stops | Multi-entity in one delegation | List every entity explicitly, or split |

## When not to delegate

When the task requires judgment, synthesis, or emotional intelligence. When you need to combine results into a narrative. When you already know the answer. When it's a conversation, not a task.

## Tool reference

### Forge (GitHub/Gitea)

**Issues:** `forge_issue_get`, `forge_issue_list`, `forge_issue_create`, `forge_issue_update` (body REPLACES entire description), `forge_issue_comment`

**PRs:** `forge_pr_get`, `forge_pr_list`, `forge_pr_diff` (default 2000 lines), `forge_pr_files`, `forge_pr_commits`, `forge_pr_reviews`, `forge_pr_review` (APPROVE/COMMENT/REQUEST_CHANGES), `forge_pr_review_comment` (inline), `forge_pr_checks`, `forge_pr_merge` (default: squash), `forge_pr_request_review`

**Other:** `forge_react` (emoji), `forge_search` (issues/code/commits)

Default repo owner is `nugget`. Pass just `"thane-ai-agent"` or full `"nugget/thane-ai-agent"`.

### Home Assistant (native profile)

| Tool | Notes |
|---|---|
| `get_state` | State + attributes. Returns 404 for nonexistent entities — use as validation. |
| `find_entity` | Search by name/keyword. Preferred for discovery. |
| `list_entities` | ALL entities by domain. 10K-83K chars — never for discovery. |
| `control_device` | Find AND control in one step. Never guess entity IDs. |
| `call_service` | Call HA services. Does NOT validate entity names — silent no-op on bad entity_id. |

**Three-step device control:** find_entity → call_service → get_state (verify). Never trust call_service success alone.

### Home Assistant (MCP tools)

`ha_search_entities`, `ha_deep_search`, `ha_get_overview` (43K chars — sparingly), `ha_get_state`, `ha_get_history`, `ha_get_statistics`, `ha_get_device`, `ha_call_service` (does NOT validate entities), `ha_bulk_control`, `ha_config_get_automation` / `set` / `remove`, `ha_get_automation_traces`, zone CRUD, `ha_list_services`

### Signal

`signal_send_message`, `signal_send_reaction`. Do NOT use `notify.signal_messenger` via HA — it doesn't exist.

### Media

`media_transcript(url, language="en", focus, detail)` — YouTube, Vimeo, podcasts. Detail levels: `full` (raw), `summary` (2-3K), `brief` (~500 chars). Raw transcript always saved to `~/Thane/transcripts/`.

## Pitfalls

- `file_read` is restricted to allowed directories. For `/tmp` or system paths, use `exec(command="cat /path")`.
- Scenes can't be deleted via call_service — requires HA UI or YAML.
- GitHub issues in thane-ai-agent are authored by `thane-agent`. Search with `author:thane-agent`.
- There is no `mcp_list_tools` or `list_tools`. All tools are already in context.
- `ha_call_service` `return_response` VALIDATION_FAILED? Retry — usually succeeds.
