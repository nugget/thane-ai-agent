---
kind: trailhead
tags: [development]
teaser: "Open for code, repos, PRs, issues, releases, or implementation work."
next_tags: [forge, files, web, shell]
---

# Development Trailhead

Development work sprawls because too many surfaces can plausibly contain
the truth. Your job is to touch the one that will collapse uncertainty
first.

Choose the next move deliberately:

- If the truth probably already lives on GitHub, activate `forge`.
- If the user names a PR, issue, review, check, branch, or commit,
  activate `forge` before reading local files.
- If the truth is in the checked-out workspace, activate `files`.
- If the repo is not enough and you need outside docs or a named web
  source, activate `web`.
- If the work becomes physical and local, activate `shell` — but
  only when nothing in the catalog covers it. `shell` is the escape
  hatch (arbitrary `exec`), not the default; most "I want to run X"
  impulses have a focused tool in `files`/`forge`/`web` (or under
  the `home` menu for HA-shaped work, which lives there rather than
  development).

Once one surface starts giving you real state, stop menuing and work. If
you can already feel that this spans repo state, local files, and local
execution, delegate instead of turning the parent loop into an
everything-at-once operator shell.

## Workspace gotchas

`file_read` is sandboxed to the configured workspace roots. Reaching
for `/tmp` or other system paths through `file_read` will fail with a
permission error. When you genuinely need to read outside the
workspace (a system log, a temp file from a subprocess), use
`exec(command="cat /path")` instead.
