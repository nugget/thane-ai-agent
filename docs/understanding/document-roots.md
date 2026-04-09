# Document Roots

Thane treats local markdown corpora as **managed document roots**.

A document root is just a named path prefix from config:

```yaml
paths:
  kb: ./knowledge
  scratchpad: ./scratchpad
  dossiers: ~/Vaults/private-dossiers
```

Each entry does two jobs at once:

- it creates a semantic reference prefix such as `kb:network/vlans.md`
- it defines a local corpus that the `documents` capability can browse,
  search, and reopen without knowing the exact path in advance

This is one of the core design ideas in Thane: local corpora should be
named, rooted, and discoverable by the model, not treated as anonymous
filesystem blobs.

## What Counts As A Root

Any configured `paths:` entry that exists on disk is eligible.

That means users can add their own document roots without code changes.
If you want Thane to reason over a new corpus, give it a prefix in
config and point it at a directory.

Examples:

- `kb:` for curated reference material
- `generated:` for reports or model-produced durable outputs
- `scratchpad:` for low-integrity working notes
- `dossiers:` for long-form private reference documents
- `research:` for imported project notes or external vault mirrors

The `core:` root is special. It is always derived from
`{workspace.path}/core` and is not configured manually.

## Why This Exists

Without roots, the model has two bad choices:

- brute-force file walking
- guessing paths from memory

Document roots give it a better path:

1. identify the corpus
2. browse or search within that root
3. inspect an outline or section
4. move to raw file reads or edits only when necessary

That is the purpose of the `documents` capability.

## `documents` vs `files`

These capabilities are related, but they are not the same.

- `documents` is for rediscovery and navigation
- `files` is for direct raw reads and edits

Use `documents` when the truth is local but the exact path has drifted
out of mind. Use `files` when the document is already known and you need
its raw content or need to modify it.

Examples:

- “Find the article about VLANs I wrote last month.”
  This is `documents`.
- “Read `kb:network/vlans.md`.”
  This is `files`.
- “Update the IoT section in `kb:network/vlans.md`.”
  This is `files`.

## What Gets Indexed

The first document-provider slice indexes markdown files under each
managed root and extracts:

- title
- summary
- tags / frontmatter values
- heading outline
- per-section content boundaries
- outbound markdown and wiki links

This is enough for the model to recover a document it already created,
find related material in the same root, and retrieve the right section
without flooding context with whole-file reads.

## Operator Guidance

If you want a corpus to be usable as a first-class local knowledge
surface:

- give it a stable prefix in `paths:`
- keep the directory rooted and coherent
- prefer markdown files for now
- use frontmatter tags when you want a local vocabulary the model can
  discover via `doc_values`

If the directory exists and is configured as a path root, Thane can
treat it as a document corpus. You do not need a separate “enable
indexing” switch.

## Example

```yaml
workspace:
  path: ~/Thane

paths:
  kb: ~/Thane/knowledge
  scratchpad: ~/Thane/scratchpad
  dossiers: ~/Vaults/private-dossiers
  research: ~/Work/research-notes
```

With that config, the model can use semantic refs like:

- `kb:network/vlans.md`
- `dossiers:people/alice.md`
- `research:mcp/indexing-notes.md`

And when it does not remember the exact path, it can still rediscover
those corpora through the `documents` capability.
