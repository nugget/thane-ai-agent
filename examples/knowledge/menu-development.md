---
tags: [development]
---

# Development Decision Tree

Use this tag when the request is broadly about software, repositories,
code changes, GitHub work, debugging, or release engineering.

Choose the next step deliberately:

- Activate `forge` when the task is about issues, pull requests, checks,
  reviews, repo metadata, or GitHub conversation state.
- Activate `files` when the task is mainly about reading or editing the
  current workspace.
- Activate `search` when you need outside docs or web confirmation.
- Activate `shell` only when local command execution is truly needed.

Stop narrowing when the visible tools already cover the task. If the job
clearly spans several of these domains, prefer `thane_delegate` with the
relevant tags instead of serially activating many tags in the top-level
loop.
