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

## A Few Practical Guidelines

- Prefer a small number of well-named roots over dozens of tiny ones.
- Keep each root internally coherent.
- Markdown is the best-supported format today.
- If a collection matters enough that you want Thane to reuse it later,
  give it a root instead of leaving it buried in a generic folder.
- If a root is very high integrity or operationally sensitive, be
  deliberate about how you want it managed and edited.

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
keeps path resolution, indexing, provenance, and future root-level
integrity policy in one subsystem.

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
