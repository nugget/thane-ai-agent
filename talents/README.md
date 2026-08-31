---
ignore: true
---

# Talents

Talents are markdown files that shape the model's posture and decision-making in
context. They are not configuration and they are not a user manual — they are
prose memory of past-self for present-self, loaded into the prompt when the
matching tag is active. See [`foundation.md`](foundation.md) for
the self-facing framing the model receives at the top of every turn.

The loader for this directory lives at
[`internal/model/talents/loader.go`](../internal/model/talents/loader.go). The
authoritative `kind:` values are declared in
[`internal/model/talents/kind.go`](../internal/model/talents/kind.go). This
README documents the authoring conventions — name your file right, choose the
right shape, and the loader does the rest.

## Every talent declares when it applies

Frontmatter is what makes a file a talent. A `.md` file in this directory that
opens with no frontmatter is not guidance and is never injected — but the
loader also names it once at boot, because a silent skip is exactly what a
talent stripped of its frontmatter would look like. A file that is
*deliberately* not a talent says so instead: open it with a frontmatter block
declaring only `ignore: true` — that is how this README sits here quietly —
and the loader skips it without comment. `ignore:` marks whole files only;
the loader refuses a file that mixes an ignore node with real guidance nodes,
because honoring it would drop that guidance silently. The filename plays no
part in any of this.

A file that does declare frontmatter must declare `tags:`, and the loader
refuses one that does not. Silence is not a shorthand for "every turn": an
absent tag list cannot be told apart from an oversight, and a talent that says
nothing about its own applicability leaves the rule invisible to whoever reads
it next. Two tags answer the question directly:

- `always` — applies to every turn, whatever shape it takes.
- `persona` — applies only where the persona is present, where the agent is
  being itself rather than executing a procedure. Not a synonym for
  "conversational": drafting an email is not a conversation but wants the
  voice.

Anything else is a capability tag, and the talent loads when that capability is
active. A talent tagged only `always` or `persona` is stable for the whole run
and lands in the prompt's long-lived prefix; mixing either with a capability tag
makes it changeable mid-run, so it moves to the shorter-lived block.

## The four kinds

Talents fall into four shapes. The kind determines how the file's content gets
used at runtime; the filename and frontmatter together signal which kind.

| Kind | Frontmatter | Filename | When it loads |
|---|---|---|---|
| **Foundation** | `tags: [always]` or `tags: [persona]` | bare topic (e.g. `presence.md`) | Every turn, or every turn wearing the persona |
| **Trailhead** | `kind: trailhead` + `next_tags:` | `<tag>-trailhead.md` | When the tag activates; sorts first |
| **Doctrine** | `tags: [...]` | `<tag>.md` (single per tag) or `<tag>-doctrine.md` | When the tag activates |
| **Examples** | `tags: [...]`, often multi-node | `<tag>-examples.md` | When the tag activates |

### Foundation

Identity, posture, and craft that should reach the model on every turn it
applies to. Six foundation files today — `foundation.md`, `presence.md`,
`awareness.md`, `communication.md`, `delegation.md`, `working-memory.md` — all
tagged `persona`, because each speaks in or about the voice.

Add a foundation talent rarely. Every line earns its always-on slot, and that
slot is a caching boundary as well as a prompt one: this content is what a
provider retains across a run.

### Trailheads

Decision-tree roots. When a tag activates, the trailhead is the
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
- `next_tags` entries must resolve to either a built-in tag in
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
tag catalog.

See [`loops-examples.md`](loops-examples.md) for the 8-node reference
implementation.

## Frontmatter reference

The loader parses these keys (others are silently ignored):

| Key | Type | Purpose |
|---|---|---|
| `name` | string | Per-talent identifier. Required in multi-node files; optional in single-node (falls back to filename). |
| `tags` | `[string, ...]` | Required. Tags that activate this talent, OR semantics. `always` and `persona` declare turn-shape applicability; anything else is a capability tag. A talent declaring none is refused at load. |
| `tags_all` | `[string, ...]` | Parsed but **not currently consulted for talent loading** — the talents loader only inspects `tags:` when deciding what to inject. `tags_all` is honored today for tagged KB articles (via the `tag_context` pipeline), where it composes AND-style with `tags:`. Leave it off talent frontmatter unless you're prepared to wire it through `Talent` and `FilterByTags` first. |
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

See [`docs/talent-authoring.md`](../docs/talent-authoring.md) for the
craft side: the decision-trail pattern, when to use multi-node trails
vs flat prose, how to write good per-leaf teasers, the "decision frame
→ 3-5 teasers" root shape, concrete JSON in leaves as the leaf
success criterion, and the anti-patterns that bit earlier PRs.

The quick orientation: read [`loops-examples.md`](loops-examples.md)
for the canonical multi-node decision tree, and [`loops.md`](loops.md)
for the canonical doctrine voice. If your draft doesn't feel like
either of those, it's probably trying to be both.
