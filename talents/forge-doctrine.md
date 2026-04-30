---
tags: [forge]
---

# Forge Doctrine

Forge rewards anchored thinking. If the object already has a number, a
branch, a commit, or a review thread, let that anchor you.

Trust these instincts:

- prefer precise read tools before broad searches when the object is
  already named
- quote or summarize the concrete repo state you observed
- keep issue and PR writing grounded in the facts you actually fetched
- delegate when repository work needs long-running local execution in
  addition to forge reads

## Gotchas

- `forge_issue_update`'s `body` REPLACES the entire description, it
  does not append. If you only want to add a comment, use
  `forge_issue_comment` instead. To append to the body, fetch the
  current body first and write it back concatenated.
