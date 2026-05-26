# Talents

Talents are markdown files that shape the model's posture and decision-making in
context. They are not configuration and they are not a user manual — they are
prose memory of past-self for present-self, loaded into the prompt when the
matching capability tag is active. See [`foundation.md`](foundation.md) for
the self-facing framing the model receives at the top of every turn.

The loader for this directory lives at
[`internal/model/talents/loader.go`](../internal/model/talents/loader.go). The
authoritative `kind:` values are declared in
[`internal/model/talents/kind.go`](../internal/model/talents/kind.go). This
README documents the authoring conventions — name your file right, choose the
right shape, and the loader does the rest.

## The four kinds

Talents fall into four shapes. The kind determines how the file's content gets
used at runtime; the filename and frontmatter together signal which kind.

| Kind | Frontmatter | Filename | When it loads |
|---|---|---|---|
| **Foundation** | none | bare topic (e.g. `presence.md`) | Always — part of the base prompt |
| **Trailhead** | `kind: trailhead` + `next_tags:` | `<tag>-trailhead.md` | When the capability tag activates; sorts first |
| **Doctrine** | `tags: [...]` | `<tag>.md` (single per tag) or `<tag>-doctrine.md` | When the capability tag activates |
| **Examples** | `tags: [...]`, often multi-node | `<tag>-examples.md` | When the capability tag activates |

### Foundation

Identity, posture, and craft that should always reach the model. No
frontmatter; the file body becomes part of the base prompt every turn. Six
foundation files today: `foundation.md`, `presence.md`, `awareness.md`,
`communication.md`, `delegation.md`, `working-memory.md`.

Add a foundation talent rarely. Every line earns its always-on slot.

### Trailheads

Decision-tree roots. When a capability tag activates, the trailhead is the
first thing the model reads about that tag — a small, opinionated map of
where to look next. See the [Trailhead section in the tools
reference](../docs/reference/tools.md#trailheads) for the canonical
definition.

- Filename: `<tag>-trailhead.md`
- Heading: `# <Domain> Trailhead`
- Frontmatter:
  ```yaml
  ---
  kind: trailhead
  tags: [<tag>]
  teaser: "One short sentence shown when the parent menu surfaces this branch."
  next_tags: [<resolvable_tag>, <resolvable_tag>, ...]
  ---
  ```
- `next_tags` entries must resolve to either a built-in capability tag in
  [`internal/model/toolcatalog/catalog_tags.go`](../internal/model/toolcatalog/catalog_tags.go)
  or a tag declared by another loaded talent. The regression test
  `TestRepoTrailheadNextTagsResolve` enforces this.

### Doctrine

The posture and instincts the model should bring to one tag's work. Reads
like a letter past-self left present-self about how this domain rewards being
worked. Concrete tool-routing belongs here; cafeteria-style "use X when Y"
bullet lists do not — they belong in an examples talent instead.

- Filename: `<tag>.md` when there's one doctrine file per tag.
- Filename: `<tag>-doctrine.md` when another non-trailhead talent shares the
  tag and disambiguation helps (e.g., `interactive-doctrine.md` lives
  alongside `interactive-communication.md`).
- Heading: `# <Domain>` (the suffix word "Doctrine" is optional; pick the
  one that reads better in context).

### Examples

Concrete patterns, often as a multi-node decision tree. `loops-examples.md`
is the canonical example — a root that walks the model through a choice with
per-leaf teasers, then concrete JSON at the leaves.

- Filename: `<tag>-examples.md`
- Often multi-node — see the next section.

## Multi-node talent files (PR #887)

A single `.md` file can hold multiple talent nodes, each with its own
frontmatter and body. The parser splits on `---` boundaries that precede a
recognized frontmatter key.

- **`name:` is required on every node in a multi-node file.** Names must be
  unique within the file. The parser errors on missing or duplicate names.
- **Single-node files** may omit `name:` and the loader falls back to the
  filename without `.md`. Existing single-node talents don't need migration.
- Each node carries its own `tags:`, `kind:`, `teaser:`, and `next_tags:`.

The shape that works:

```
trailhead-root  →  (decision frame + per-leaf teasers)
   ├── leaf_shape_a  →  concrete shape + JSON template
   ├── leaf_shape_b  →  concrete shape + JSON template
   └── leaf_shape_c  →  (if needed) further fork to depth-2 leaves
```

Each leaf carries its own `name:` and `tags: [own_name]`, so the parent
trailhead's `next_tags` can target it without polluting the global
capability-tag catalog.

See [`loops-examples.md`](loops-examples.md) for the 8-node reference
implementation.

## Frontmatter reference

The loader parses these keys (others are silently ignored):

| Key | Type | Purpose |
|---|---|---|
| `name` | string | Per-talent identifier. Required in multi-node files; optional in single-node (falls back to filename). |
| `tags` | `[string, ...]` | Capability tags that activate this talent. OR semantics. |
| `tags_all` | `[string, ...]` | Capability tags that must all be active for this talent to load. AND semantics. Composes with `tags:` when both are set. |
| `kind` | `trailhead` or empty | Marks the file as a trailhead. The legacy `entry_point` value still loads with a deprecation warning. |
| `teaser` | string | One-line summary shown when a parent menu surfaces this branch. Trailheads should set this. |
| `next_tags` | `[string, ...]` | Suggested follow-on tags. Trailheads use this to chain decision steps. Must resolve to built-in tags or talent-declared tags. |

## Regression tests

Two tests in [`internal/model/talents/repo_corpus_test.go`](../internal/model/talents/repo_corpus_test.go)
guard against drift:

- **`TestRepoTrailheadNextTagsResolve`** — every trailhead's `next_tags`
  must resolve to a real tag (built-in or talent-declared).
- **`TestRepoTalentToolReferences`** — backticked tool-name references in
  talent prose must match a registered tool from the catalog. Catches
  hallucinations like the made-up email_compose or watch_entity (deliberately
  unbackticked here so the regression test doesn't fire on this README).

Both run as part of `just ci`. If you add a tool reference the test flags as
a false positive (a backticked snake_case term that isn't a tool — e.g., a
field name on a config struct), add it to the `nonToolTokens` allowlist with
a comment explaining why.

## Authoring guidance

Coming in a follow-on PR: a deeper guide on the decision-trail pattern —
when to use multi-node trails, how to write good per-leaf teasers, the
"decision frame → 3-5 teasers" root shape, and the success criterion of
concrete JSON in leaves. Until then, read `loops-examples.md` for the
canonical example and `loops.md` for the canonical doctrine voice.
