---
tags: [forge]
---
# Forge Workflows

Patterns for working with GitHub/Gitea through forge tools. All forge tool responses are JSON — parse structured fields, read body/description as prose after the `---` separator.

Forge tools operate directly against the GitHub/Gitea API. Most operations don't need local repo access or shell commands — the tools handle authentication, pagination, and response formatting.

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

**Creating PRs**: Use `forge_pr_create` with the branch names and description:
```
forge_pr_create(
  repo: "thane-ai-agent",
  title: "feat: add behavioral lens system",
  body: "temp:pr-description",
  head: "feature-branch",
  base: "main"
)
```
For large PR descriptions, compose the body with `create_temp_file` first.

**Merging PRs**: `forge_pr_merge(number: 595)` defaults to squash merge. Pass `method: "merge"` or `method: "rebase"` for other strategies. Use `commit_title` and `commit_message` to customize the merge commit.

## Search

`forge_search(query: "...", kind: "issues" | "code" | "commits")` searches across the configured forge. Results are JSON with number, title, URL, and body snippet.

**Search operators**: GitHub search syntax works — `is:open`, `is:closed`, `author:thane-agent`, `label:enhancement`, `created:>2026-03-01`, etc. Always search before creating issues to avoid duplicates.

## When to delegate

Most forge operations work well from the core model — `forge_issue_get`, `forge_pr_get`, `forge_search` are single API calls that return concise JSON. Use these directly.

Delegate when you need to **explore broadly without burning core context**: surveying many issues, reading multiple PR diffs, or doing bulk operations where the intermediate data is large but the desired output is a summary.

```
thane_delegate(
  task: "Survey all open issues labeled 'enhancement' on thane-ai-agent and write a one-paragraph summary of themes",
  tags: ["forge"]
)
```

Delegation is also useful when the task needs **local file access** alongside forge data (e.g., reading source code to understand an issue). Use `path_prefixes` so the delegate has the repo:

```
thane_delegate(
  task: "Read the router code and assess whether issue #93 Phase 1 is still accurate",
  tags: ["forge", "files"],
  path_prefixes: {"repo": "~/Sync/Projects/AI/Claude/thane-ai-agent"}
)
```
