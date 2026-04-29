# Document Roots

When Thane works well, your documents feel easy to refer to and easy to
find again.

That starts with **document roots**: named directories in config that
tell Thane which local collections matter.

```yaml
paths:
  kb: ./knowledge
  scratchpad: ./scratchpad
  dossiers: ~/Vaults/private-dossiers
```

Each entry gives a directory a stable identity. Instead of treating your
files as one anonymous pile, Thane can understand that some notes live
in a knowledge base, some are scratch work, and some belong to a more
private long-form collection.

## Why This Helps

The point is not to make you think about implementation details. The
point is to make everyday use feel smoother.

Good document roots help when:

- you want Thane to keep track of a body of notes over time
- you have several different kinds of material and want them kept
  distinct
- you create something today and want to be able to find it again later
- you want to refer to a collection by name instead of by fragile paths

If you give a directory a stable root, Thane has a better chance of
finding the right thing without you needing to remember the exact file
location yourself.

## What To Put In A Root

A document root should be a coherent collection, not just a convenient
folder.

Good examples:

- `kb:` for durable reference material
- `scratchpad:` for rough working notes
- `generated:` for reports and other machine-produced outputs
- `dossiers:` for long-form background material on people, projects, or
  places
- `research:` for a project-specific note collection

Less good examples:

- a giant home directory with unrelated files mixed together
- a temporary folder that changes shape constantly
- a directory whose contents you do not actually want Thane treating as
  part of its working world

The cleaner the boundary, the easier it is for Thane to stay oriented.

## Adding Your Own Roots

You do not need any special indexing section or separate feature flag.

If a directory is listed in `paths:` and exists on disk, it becomes one
of Thane's managed local document collections.

Example:

```yaml
workspace:
  path: ~/Thane

paths:
  kb: ~/Thane/knowledge
  scratchpad: ~/Thane/scratchpad
  dossiers: ~/Vaults/private-dossiers
  research: ~/Work/research-notes
```

With a setup like this, you can gradually build several stable document
collections without changing code or teaching Thane a new subsystem each
time.

## Root Policy

Most roots do not need extra policy. If a root is listed in `paths:` and
exists on disk, Thane indexes markdown in that root and managed document
tools may write it.

When a root needs a stronger contract, add `doc_roots:`:

```yaml
paths:
  kb: ~/Thane/knowledge
  scratchpad: ~/Thane/scratchpad

doc_roots:
  kb:
    authoring: managed
    git:
      enabled: true
      sign_commits: true
      verify_signatures: warn
      signing_key: ~/.ssh/id_ed25519
  scratchpad:
    indexing: false
    authoring: managed
```

Policy is deliberately attached to the root, not to individual tools or
prompts. A loop-declared output, a direct document write, and the
corpus-aware intake flow should all meet the same root contract.

The current policy fields are:

- `indexing`: set `false` when a root may be written/read by exact ref
  but should stay out of browse/search results.
- `authoring`: `managed` allows document mutations, `read_only` blocks
  them, and `restricted` reserves the root for narrower future flows.
- `git.enabled`: records that the root participates in git-backed
  provenance.
- `git.sign_commits`: signs and commits each managed write/delete.
- `git.verify_signatures`: sets the consumer policy: `none`, `warn`,
  or `required`.

Signature-required roots are the place for high-integrity authored
knowledge, such as owner-tagged knowledge articles. When verification
is `required`, Thane blocks the following content paths when the
target content is not cleanly covered by trusted signed git history:

- Document store reads (`Read`, indexed browse and search surfaces)
- Loop-declared output context
- Tagged context articles
- The model's `read_file` tool when the resolved path lies inside a
  managed root
- Startup-time inject-files (the fixed core context the agent sees on
  every turn)
- Startup-time talents (behavioral guidance markdown loaded from the
  configured talents directory)

When verification is `warn`, Thane records and logs verification
failures but still lets the content load.

The raw `write_file` and `edit_file` tools are stricter than read
verification. They cannot mutate read-only/restricted roots, and they
cannot mutate roots with signed git provenance; those changes must go
through managed document tools so root writers can preserve authoring
policy, git history, and signatures.

Directory-walk surfaces (`list_files`, `tree`, `stat`, `search_files`,
`grep`) intentionally do not consult the verifier. `list_files`,
`tree`, `stat`, and `search_files` return only paths and metadata.
**`grep` is different**: it returns short content excerpts that are
*not* verified, so under `verify_signatures: required` it can surface
snippets from files that would be blocked by `read_file`. If a result
matters to you, re-read it through `read_file` — that path is gated
and will fail if the underlying content is not covered by trusted
signed history.

## A Few Practical Guidelines

- Prefer a small number of well-named roots over dozens of tiny ones.
- Keep each root internally coherent.
- Markdown is the best-supported format today.
- If a collection matters enough that you want Thane to reuse it later,
  give it a root instead of leaving it buried in a generic folder.
- If a root is very high integrity or operationally sensitive, be
  deliberate about how you want it managed and edited.

## Corpus-Aware Intake

When Thane is about to create durable knowledge, the safest first step
is `doc_intake`, not guessing a new filename. Intake looks at the target
root, searches related documents, checks observed tag vocabulary and
path patterns, and returns a proposed destination with a recommended
action:

- `create_new` when the corpus does not appear to contain the idea yet
- `update_existing` when a related document looks like the better home
- `append_existing` when the request sounds like a journal or running
  note
- `draft_for_review` when root policy or ambiguity makes a write
  inappropriate

The result includes an `intake_id`, normalized title/tags/frontmatter,
related documents with similarity scores, and a `commit_plan`. The
model then calls `doc_commit` with that `intake_id` and the approved
body. If intake reports a caution, such as a high-overlap existing
document, `doc_commit` requires `confirm=true` before writing.

This keeps taxonomy decisions anchored in the existing corpus. The raw
mutation tools still exist for deliberate exact-path work, but intake is
the normal authoring flow for new knowledge.

## Generated Documents

Generated markdown should be legible to both people and the document
index. When Thane intentionally writes an artifact into a managed root,
the file should include document-local provenance in frontmatter:

```yaml
generated_by: "media_save_analysis"
generated_at: "2026-04-26T18:14:15Z"
document_kind: "media_analysis"
refresh_strategy: "immutable"
source_refs:
  - "url:https://example.test/watch?v=abc123"
  - "feed:security-news"
managed_root: "generated"
```

These fields answer a different question than git history. Frontmatter
tells Thane what kind of generated artifact it is, what source material
it came from, and how future refreshes should treat it. Root-level git
history and signature policy can still answer who changed a file and
whether that change is trusted.

Use `source_refs` for compact, typed references such as URLs,
conversation IDs, Home Assistant entity IDs, attachment hashes, or feed
IDs. Use `refresh_strategy` to distinguish one-shot immutable artifacts
from generated files that are replaced, appended to, or maintained as a
rolling window.

## Loop-Declared Outputs

Autonomous and background loops can declare the documents they are
responsible for maintaining. Thane turns those declarations into narrow
runtime tools:

- `replace_output_<name>` for a maintained document that should be
  rewritten as a complete current state.
- `append_output_<name>` for a journal document that should receive new
  entries over time.

The loop sees a matching context block with the current document content
or recent journal tail, so the document itself remains the durable source
of truth. The generated tools still write through document roots. That
keeps path resolution, indexing, provenance, and root-level integrity
policy in one subsystem.

## Special Case: `core`

The `core:` root is reserved.

It always comes from `{workspace.path}/core` and is not configured
manually in `paths:`. That is where Thane's always-on identity and core
reference files live.

## The Human-Level Rule

If you find yourself thinking:

- “this directory is part of Thane's long-term world”
- “I want to be able to refer to this collection by name”
- “I do not want this to get lost just because the exact path slips my
  mind”

then it probably wants to be a document root.
