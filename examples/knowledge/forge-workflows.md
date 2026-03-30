---
tags: [forge]
---
# Forge Workflows

Patterns for working with GitHub/Gitea through forge tools. All forge tool responses are JSON — parse structured fields, read body/description as prose after the `---` separator.

## Issue management

**Reading issues**: `forge_issue_get` returns JSON with number, state, author, labels, assignees, comment count, delta timestamps, and the full body. One call gives you everything.

**Updating issue descriptions**: The `body` parameter on `forge_issue_update` REPLACES the entire description. To update an issue description:

1. `forge_issue_get` to read the current body
2. Compose the new body (keeping what you want, changing what you need)
3. `create_temp_file(label: "issue-NNN", content: "<full new body>")`
4. `forge_issue_update(number: NNN, body: "temp:issue-NNN")`

The `temp:` reference resolves to the full file content automatically. Never try to inline a large body directly in tool arguments.

**Adding context without replacing**: Use `forge_issue_comment` for status updates, observations, or notes. Comments are additive. Issue body is the canonical description; comments are the conversation.

## Pull request workflow

**PR inspection**: `forge_pr_get` includes review summary ("2 approved, 1 changes requested"), check summary ("5 passed"), draft status, labels, assignees, and requested reviewers — all inline. No need for separate `forge_pr_reviews` or `forge_pr_checks` calls unless you need per-review detail.

**Code review**: For reviewing a PR:
1. `forge_pr_get` — understand the PR (includes review/check status)
2. `forge_pr_diff` — read the actual changes (truncated at 2000 lines by default)
3. `forge_pr_files` — see which files changed and their stats
4. `forge_pr_review(event: "APPROVE" | "COMMENT" | "REQUEST_CHANGES", body: "...")`

**Creating PRs**: Use `exec` with `gh pr create` — there's no native forge PR creation tool. Compose the PR body with `create_temp_file` first if it's substantial.

## Delegation patterns

**Tag-scoped forge work**: `thane_delegate(task: "...", tags: ["forge"])` gives the delegate all forge tools. Use `path_prefixes` for repo checkouts:

```
thane_delegate(
  task: "Review PR #595 on thane-ai-agent",
  tags: ["forge"],
  path_prefixes: {"repo": "~/Sync/Projects/AI/Claude/thane-ai-agent"}
)
```

The delegate gets a directory listing of the prefix path in its context, eliminating the first `file_list` call.

**Passing context to delegates**: For tasks that need background (diffs, issue bodies, prior discussion):

```
create_temp_file(label: "pr-595-context", content: "<assembled context>")
thane_delegate(
  task: "Review the changes in temp:pr-595-context and suggest improvements",
  tags: ["forge"]
)
```

## Search

`forge_search(query: "...", kind: "issues" | "code" | "commits")` searches across the configured forge. Results are JSON with number, title, URL, and body snippet. Use `kind: "issues"` to find related issues before creating new ones.
