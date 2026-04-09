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
