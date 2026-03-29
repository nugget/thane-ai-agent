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

## Tool-specific gotchas

- `forge_issue_update` body REPLACES entire description — not a patch
- `call_service` does NOT validate entity names — silent no-op on bad entity_id
- `list_entities` returns 10K-83K chars — use `find_entity` for discovery instead
- `file_read` is restricted to allowed directories — use `exec(command="cat /path")` for system paths
- There is no `list_tools` or `mcp_list_tools` — all tools are already in your context
