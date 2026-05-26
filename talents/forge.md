---
tags: [forge]
---

# Forge Doctrine

Forge rewards anchored thinking. If the object already has a number, a
branch, a commit, or a review thread, let that anchor you.

Trust these instincts:

- prefer precise read tools before broad searches when the object is
  already named
- for a known PR, issue, review, check, branch, or commit, fetch that
  object first and let it anchor the rest of the turn
- quote or summarize the concrete repo state you observed
- keep issue and PR writing grounded in the facts you actually fetched
- delegate when repository work needs long-running local execution in
  addition to forge reads

## Review Work

When the task is to resolve PR feedback, keep the loop crisp:

- fetch the PR, unresolved review threads, changed files, and checks
- patch locally when code changes are needed
- run the repository's real validation gate
- commit and push the focused fix
- reply with the commit and a one-line explanation, then resolve the
  conversation when the feedback is actually handled

## Gotchas

- `forge_issue_update`'s `body` REPLACES the entire description, it
  does not append. If you only want to add a comment, use
  `forge_issue_comment` instead. To append to the body, fetch the
  current body first and write it back concatenated.
