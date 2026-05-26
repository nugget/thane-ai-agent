---
tags: [files]
---

# Files

Use `files` to let the document speak before you compress it.

If a named document or semantic path is already in hand, the best next
move is usually to read it before widening scope.

## The single most important disambiguation

**`files` is raw filesystem access inside the workspace. `documents`
is the managed-document layer with semantic refs, frontmatter, and
index hygiene. The two share the word "file" but do different jobs.**

| You want... | Surface |
|---|---|
| Read or write a workspace file at a raw path (`./README.md`, `./Cargo.toml`, build outputs, config) | `files` (`file_read`, `file_write`, `file_grep`, etc.) |
| Read, edit, or search inside a managed root (`kb:`, `core:`, `scratchpad:`, `dossiers:`, etc.) | `documents` (`doc_read`, `doc_edit`, `doc_search`, etc.) |
| Read outside the workspace sandbox (system logs, `/etc/`, temp files from subprocesses) | `shell` (`exec`) — the one case where `exec` is the right tool over a focused one |
| Store a compact key+value that survives the conversation | `memory` (`remember_fact`) — files are not where short truths belong |

The rule: if the path you're touching has a semantic ref
(`kb:network/vlans.md`, `dossiers:people/alice.md`), use
`documents`. If it has a raw filesystem path inside the workspace,
use `files`. If it has a path outside the workspace, the work
routes through `shell`. Note the `.md` extension — managed refs are
canonical *with* the extension; passing `kb:foo` to a `doc_*` tool
will fail "document not found."

## Trust these instincts

- preserve semantic references like `kb:article.md` and `core:persona.md`
- read the named document before widening to the web when a reference
  is already known
- quote or summarize from what you actually read
- keep long documents in `files`, not `memory`
