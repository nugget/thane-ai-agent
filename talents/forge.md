---
name: forge
tags: [forge]
kind: trailhead
teaser: "Open for code-forge work — issues, PRs, reviews, checks, releases, subscriptions."
next_tags: [forge_known_issue, forge_known_pr, forge_discover, forge_review_loop]
---

# Forge

Forge rewards anchored thinking. If the object already has a number, a
branch, a commit, or a review thread, let *that* anchor the turn.
Fetch the concrete state first, then quote or summarize what you
actually observed; never write an issue or PR comment grounded in
remembered shapes instead of fetched ones.

## Choose by what you have

Activate the next tag based on what's already anchored:

- **You have an issue number** — activate `forge_known_issue`. Read,
  comment, update, react on an issue you can name.

- **You have a PR number** — activate `forge_known_pr`. Read the PR
  surface (get/diff/files/commits/checks/reviews), submit reviews as a
  reviewer, drive lifecycle as an author (request reviewers, merge),
  add reactions.

- **Nothing's anchored yet** — activate `forge_discover`. Search,
  list, browse repo subscriptions, create a new issue, follow a repo
  for event-driven wakes.

- **You're resolving feedback on a PR you authored** — activate
  `forge_review_loop`. The canonical fetch → patch → CI → reply →
  resolve workflow. Distinct enough from generic known-PR work that it
  gets its own trail.

## Delegate when the work outgrows reads

A forge turn that needs long-running local execution (clone, build,
run the gate, push) on top of forge reads is delegate-shaped, not
inline. `thane_assign` or `thane_now` from `loops_examples` is usually
the right move once the loop expands past a few reads and a comment.

---
name: forge_known_issue
tags: [forge_known_issue]
kind: trailhead
teaser: "Read, comment, update, or react on an issue you have the number for."
---

# Known issue

You have `repo` and `number`. Anchor with `forge_issue_get` before
writing anything — the issue body, current state, labels, and assignees
are the ground truth your comment or update has to agree with.

## Fetch first

```json
{
  "repo": "nugget/thane-ai-agent",
  "number": 910
}
```

Returns title, body, state, labels, assignees, timestamps. Read the
body before commenting; read the labels before relabeling.

## Comment vs. update — they're different tools

`forge_issue_comment` appends a *new* comment to the issue's
conversation:

```json
{
  "repo": "nugget/thane-ai-agent",
  "number": 910,
  "body": "PR-D landed; PR-E in flight as a stacked branch. Updating the tracking checklist on the issue body next."
}
```

`forge_issue_update` mutates the issue's *own fields* — title, body,
state, labels:

```json
{
  "repo": "nugget/thane-ai-agent",
  "number": 910,
  "state": "closed",
  "labels": ["done", "talents"]
}
```

## Gotcha: body REPLACES, it does not append

`forge_issue_update` with a `body` field overwrites the entire issue
description. If you want to extend the description, fetch the current
body via `forge_issue_get` first and write it back concatenated. The
`labels` field is the same — passing labels replaces the full set, not
adds to it.

When the goal is "append a thought to the conversation," use
`forge_issue_comment` instead — that's almost always what you actually
want.

## How the next reader will see this

When a future model picks up this issue, it reads the title and the
description — not the comment thread. That makes the body the
**living spec**, not just the original framing.

- As scope evolves through discussion, fold the changes back into the
  body via `forge_issue_update`. A comment that says "actually, let's
  also handle X" is invisible to the next implementer unless someone
  edits it into the body.
- Comments are conversation history, useful for audit and provenance,
  but they don't drive the work. Plan for the description to carry
  the full current intent on its own.

The same logic applies in reverse for PRs you authored: keep the PR
body honest about what actually landed, because the next reviewer
reads the description, not the running commentary.

## Lightweight acknowledgement

`forge_react` adds an emoji reaction to the issue itself or to one of
its comments. Useful when the right answer is "noted" or "agreed"
without a new comment:

```json
{
  "repo": "nugget/thane-ai-agent",
  "number": 910,
  "emoji": "+1"
}
```

Pass `comment_id` to react to a specific comment rather than the
issue.

---
name: forge_known_pr
tags: [forge_known_pr]
kind: trailhead
teaser: "Read the PR surface, review as a reviewer, drive lifecycle as the author, or react."
---

# Known PR

You have `repo` and `number`. The PR surface is wider than an issue's
— there's diff, files, commits, checks, and review threads on top of
the base metadata. Pick by what you're trying to learn or do.

## Read the PR

`forge_pr_get` returns metadata only (title, body, state, branches,
mergeable status, additions/deletions). Start here:

```json
{
  "repo": "nugget/thane-ai-agent",
  "number": 914
}
```

For the actual code changes, pick by size:

- `forge_pr_diff` — unified diff in one payload (truncated at 2000
  lines by default; raise `max_lines` for slightly larger or fall
  through to `forge_pr_files` for big PRs):

  ```json
  {
    "repo": "nugget/thane-ai-agent",
    "number": 914,
    "max_lines": 4000
  }
  ```

- `forge_pr_files` — per-file patches with status, additions,
  deletions. Right for large PRs or when you only care about specific
  paths:

  ```json
  {
    "repo": "nugget/thane-ai-agent",
    "number": 914
  }
  ```

- `forge_pr_commits` — commit list with SHA, message, author, date.
  Useful when the PR history matters (reverts, fixup commits, hash
  references):

  ```json
  {
    "repo": "nugget/thane-ai-agent",
    "number": 914
  }
  ```

- `forge_pr_reviews` — review submissions plus their inline comments
  nested underneath. This is where Copilot review feedback lives:

  ```json
  {
    "repo": "nugget/thane-ai-agent",
    "number": 914
  }
  ```

- `forge_pr_checks` — CI runs with status and conclusion:

  ```json
  {
    "repo": "nugget/thane-ai-agent",
    "number": 914
  }
  ```

## Review as a reviewer

When you're submitting a review on someone else's PR, use
`forge_pr_review`:

```json
{
  "repo": "nugget/thane-ai-agent",
  "number": 914,
  "event": "APPROVE",
  "body": "Clean conversion; the multi-node shape reads well and the JSON in each leaf is adaptable. Approving."
}
```

`event` is `APPROVE`, `COMMENT`, or `REQUEST_CHANGES`. For inline
comments at specific diff lines (the shape Copilot uses), use
`forge_pr_review_comment` instead:

```json
{
  "repo": "nugget/thane-ai-agent",
  "number": 914,
  "path": "talents/documents.md",
  "line": 74,
  "body": "Name doc_outline explicitly here — the JSON block is shape-identical to a doc_read call."
}
```

## Drive lifecycle as the author

`forge_pr_request_review` requests reviews from specific users:

```json
{
  "repo": "nugget/thane-ai-agent",
  "number": 914,
  "reviewers": ["nugget"]
}
```

`forge_pr_merge` merges; default method is squash:

```json
{
  "repo": "nugget/thane-ai-agent",
  "number": 914,
  "method": "squash"
}
```

Prefer squash for talent/doc PRs; reach for `merge` or `rebase` only
when the commit history on the branch carries semantic value worth
preserving.

## React

Same shape as issue reactions — `forge_react` works on PRs and on
specific PR comments via `comment_id`.

---
name: forge_discover
tags: [forge_discover]
kind: trailhead
teaser: "Search, list, browse subscriptions, create a new issue, or follow a repo for event-driven wakes."
---

# Discover

Nothing's anchored yet. Three flavors of unanchored work:

## Search the forge

`forge_search` runs the forge's native query syntax. Pick `kind`
deliberately:

```json
{
  "query": "is:open is:pr review-requested:@me",
  "kind": "issues",
  "limit": 20
}
```

`kind` is `issues` (covers issues and PRs), `code`, or `commits`. The
query syntax is whatever GitHub/Gitea natively support — `is:open
label:bug`, `repo:nugget/thane-ai-agent`, `author:nugget`, etc.

## List a repo's objects

When you know the repo, list is cheaper and more focused than search.

`forge_issue_list`:

```json
{
  "repo": "nugget/thane-ai-agent",
  "state": "open",
  "labels": "bug,talents",
  "limit": 30
}
```

`forge_pr_list`:

```json
{
  "repo": "nugget/thane-ai-agent",
  "state": "open",
  "base": "main",
  "limit": 30
}
```

Both accept `sort` (`created`/`updated`/`comments`) and `direction`
(`desc`/`asc`).

## Create an issue when search finds nothing

`forge_issue_create` is the right call once you've confirmed no
existing tracker covers the work:

```json
{
  "repo": "nugget/thane-ai-agent",
  "title": "Migrate forge.md to multi-node tree",
  "body": "Tracking PR-E of the talent overhaul (#910). Follows the loops-examples shape.",
  "labels": ["talents"]
}
```

## Follow a repo for event-driven wakes

`forge_repo_follow` wakes a loop on new releases and/or commits. Use
after identifying or creating a `thane_loop_create` service loop
(`operation="service"`) that owns the output — see
`loops_examples_curate` for the service-loop shape. The output document
is optional now, so a service loop can consume these wakes without
maintaining one.

```json
{
  "repo": "nugget/thane-ai-agent",
  "track_releases": true,
  "track_commits": false,
  "wake_loop": {"name": "release_digest"}
}
```

`forge_repo_subscriptions` lists what's currently followed (with the
`subscription_id` you'll need to unfollow); `forge_repo_unfollow`
removes one.

---
name: forge_review_loop
tags: [forge_review_loop]
kind: trailhead
teaser: "Resolve review feedback on a PR you authored — fetch, patch, CI, commit, reply, resolve."
---

# Review feedback loop (as the PR author)

When the task is to resolve feedback on a PR you authored, keep the
loop crisp. The shape that works:

## 1. Fetch the concrete state

Anchor on the PR and its unresolved reviews before touching code:

```json
{
  "repo": "nugget/thane-ai-agent",
  "number": 914
}
```

(`forge_pr_get` for metadata, `forge_pr_reviews` for the inline
threads, `forge_pr_files` if the feedback references specific paths,
`forge_pr_checks` if CI is part of the conversation.)

For unresolved review thread IDs — which forge doesn't surface
directly — drop to GraphQL:

```bash
gh api graphql -f query='query { repository(owner: "nugget", name: "thane-ai-agent") { pullRequest(number: 914) { reviewThreads(first: 30) { nodes { id isResolved comments(first: 5) { nodes { id author { login } body path line } } } } } } }'
```

## 2. Patch locally

Make the focused change in the working tree. One thread → one logical
fix. Don't bundle unrelated cleanups into a review-response commit
— that turns one reviewable diff into a noisy one.

## 3. Run the real validation gate

Whatever the repo's CI gate is (`just ci`, `npm test`, the project's
own command), run it locally. Don't push assuming GitHub Actions will
catch problems — the gate is the first line of defense, not the safety
net.

## 4. Commit and push the focused fix

Conventional-commits style, message that names *what was wrong* and
*why this fixes it*. The reviewer should be able to read the commit
message and know the comment was understood.

## 5. Reply with the commit hash; then resolve

For each thread you addressed, post a one-line reply with the fixing
commit hash and resolve the thread:

```bash
gh api repos/nugget/thane-ai-agent/pulls/914/comments/3300766456/replies \
  -f body="Fixed in 4433527 — doc_outline now named explicitly..."
gh api graphql -f query='mutation { resolveReviewThread(input: {threadId: "PRRT_..."}) { thread { isResolved } } }'
```

The reply and resolve are separate calls; both matter. Replying
without resolving leaves the thread visually open; resolving without
replying loses the audit trail.

For threads you're *deferring* (out of scope, follow-up issue), say so
explicitly in the reply *before* resolving. Silent deferrals look like
silent dismissals to the reviewer.

## Delegate when the patch is large

If the fix needs a long-running local pass (regenerate fixtures, run
migrations, rebuild assets) beyond the forge reads and a small code
edit, consider `thane_assign` or `thane_now` instead of doing the work
inline. See `loops_examples` for the delegate shapes.
