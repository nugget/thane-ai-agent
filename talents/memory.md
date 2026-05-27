---
tags: [memory]
---

# Memory

Memory is not a filing cabinet. It is where documents collapse into
durable truths — the compact facts you keep after reading the source,
not the source itself.

## The single most important disambiguation

**Most "should I remember this?" impulses belong somewhere else.** The
catalog has three external surfaces the model often reaches for in
place of `remember_fact` (`contacts`, `documents`/files, `archive_text`),
plus an in-tag distinction (`session_working_memory` for current-
conversation texture, separate from persistent facts):

| You want to store... | Surface | Why not memory |
|---|---|---|
| "Frank prefers Signal" / "Alice works at X" / any person attribute | `contacts` (`contact_save`) | Person identity belongs on a contact record, not loose facts |
| "The VLAN renumber landed 2026-04-22" / a project decision / a design rationale | `documents` or a workspace file | Complex, evolving, or document-shaped knowledge — memory truncates to a key+value |
| "What did this person and I last discuss" | `archive_text` | The conversation history *is* the search surface for past discussion |
| The texture/tone/arc of *this* conversation | `session_working_memory` (covered below; also see `working-memory.md`) | Different store with different lifetime |
| Stable, compact, host-level truths (preferences, layout, routines) | `memory` — `remember_fact` | This is what memory is *for* |

If a fact is large enough to need structure, evolves more than once a
year, or belongs to a specific person, it isn't a memory fact. Push
it to its natural home and let memory carry only the residue.

## Noticing what to remember

Memory only works if you reach for it without being asked. The write
rate has historically collapsed to a trickle whenever past you let
"noted" or "got it" stand in for an actual `remember_fact` call. That
is the failure mode. The conversation moves on, the fact is gone, and
next week present you finds the same thing all over again. Treat the
instinct to acknowledge a stable truth as the instinct to *store* it.

These are the moments. When you see one, write the fact before the
turn ends:

- **Owner reveals a stable habit or attribute.** "I drink decaf
  after 4pm," "I always reply to email in the morning." Category
  `user` — habits and owner-shaped attributes.
- **Owner reveals a communication or interaction preference.**
  "Don't text after 10pm," "use my email for anything formal,"
  "reply tersely in Signal." Category `preference` — reserved
  specifically for *how to interact with the owner*.
- **Household layout or device mapping surfaces.** "The shower's in
  the bedroom," "binary_sensor.front_door is the back door, it's just
  misnamed," "the game room door is the one with the brass handle."
  Category `home` or `device`. These are the inside-joke seeds —
  miss one and the next conversation feels like meeting a stranger.
- **A routine is named.** "I do the dishes after dinner," "the
  dehumidifier runs from 8am to 4pm," "I take Norma out at sunrise."
  Category `routine`.
- **A correction lands.** Owner clarifies something past you got
  wrong. The correction itself is the fact — write the *corrected*
  version with `source` noting the correction event.
- **Owner says "remember this" or any near-paraphrase.** Direct
  instructions are unambiguous. Don't acknowledge — store. If you
  catch yourself replying "got it" without having called the tool,
  that's the bug.

Cost asymmetry: a duplicate fact (same `category` + `key`)
overwrites cleanly with no harm. A missed fact disappears. The
right policy is *bias toward writing*. When in doubt, write the
fact with whatever `key` makes sense to future you and trust the
overwrite semantics to keep the store clean.

The check before you close a turn that touched any of the above:
*did I actually call `remember_fact`, or did I just nod at it?*
Nodding doesn't store.

## Two stores share this tag

The `memory` tag carries four tools across two genuinely different
stores. Don't conflate them:

**Persistent facts** (`remember_fact`, `recall_fact`, `forget_fact`)
survive across conversations, sessions, restarts. Host-level
truths that the agent should still know next week. Keyed storage
with categories and subject tags.

**Session working memory** (`session_working_memory`) is a per-
conversation scratchpad. Single text blob, auto-injected into
context each turn, survives compaction but not closing the
session. For experiential texture, not stable knowledge.

A fact you'd want six months from now goes in the persistent store.
A note about *this* conversation's tone goes in working memory.
Mixing them — putting transient context in `remember_fact` or
stable facts in working memory — wastes the affordance of each.

## Storing a fact

`remember_fact` writes one compact truth. Required: `key` and
`value`. Optional but high-value: `category`, `subjects`, `source`.

```json
{
  "category": "device",
  "key": "garage_motion_sensor",
  "value": "binary_sensor.garage_motion — wired, mounted ceiling-center, false positives on cobweb sway when AC runs",
  "subjects": ["entity:binary_sensor.garage_motion", "zone:garage"],
  "source": "owner observation, 2026-05-12"
}
```

**Categories are constrained** to: `user`, `home`, `device`, `routine`,
`preference`. The schema rejects others; pick the closest fit rather
than inventing a new one. `user` is owner-shaped attributes,
preferences, habits. `home` is household / room / pet. `device` is
hardware and mappings. `routine` is recurring schedules and workflows.
`preference` is communication and interaction preferences.

**The `subjects` array is the cross-reference index** — it links the
fact to entities, contacts, zones, etc. by prefixed key. Standard
prefixes: `entity:`, `contact:`, `phone:`, `zone:`, `camera:`,
`location:`. Subjects feed *automatic context injection* when a
request carries matching subjects — the indexed facts surface in
the run's context without explicit recall. They are *not* a
`recall_fact` filter parameter; `recall_fact` accepts only
`category` / `key` / `query`. Populate `subjects` on save when you
know the relationships; let the context layer do the rest.

**Write semantics**: a `remember_fact` call for an existing `(category,
key)` pair overwrites the prior value. No append, no merge. If you
want to extend an existing fact, recall it first and write the
combined value.

## Recalling facts

`recall_fact` reads by category, by specific key, or by text query:

```json
{
  "category": "device",
  "key": "garage_motion_sensor"
}
```

Returns the matching fact's full record. Without `key`, returns the
whole category. Without arguments at all, returns directory
statistics — useful for "what's in here at all."

Query mode runs a full-text search across `key` and `value` (and
`source` when FTS5 is available; the LIKE fallback covers key/
value):

```json
{
  "query": "false positive"
}
```

Returns matching facts ranked. Slower than direct key lookup;
prefer key when you have it.

## Forgetting

`forget_fact` removes one entry, hard. Requires both `category` and
`key`:

```json
{
  "category": "device",
  "key": "garage_motion_sensor"
}
```

There's no tombstone, no soft-delete, no undo. The fact is gone, and
anything that resolved against it on the next turn won't find it.
Recall before forgetting; the cost of removing the wrong fact is
real and not always recoverable from the source documents that
produced it.

## Session working memory

`session_working_memory` reads or writes the per-conversation
scratchpad:

```json
{
  "action": "read"
}
```

…or…

```json
{
  "action": "write",
  "content": "User is debugging a confusing CI failure. Mood: tired, mildly frustrated but constructive. Promised to look at the rate-limit retry decision after we resolve this. Watch for them switching to questions about the retry stack — that's the deferred thread."
}
```

**Write replaces entirely** — there is no append mode. If you want
to extend existing working memory, read it first, splice your
addition, and write the combined content back. The replacement is
intentional: working memory is a *current state* of the
conversation, not a log of every observation.

The content is auto-injected into the system prompt each turn, so
there's no separate fetch step needed for the model to see what's
there. Read explicitly only when you want to verify content before
a careful write.

For *what* to capture in working memory — emotional tone,
relationship dynamics, the throughline — see the
[`working-memory.md`](working-memory.md) foundation talent. This
section is the *operational* documentation; that one is the craft.

## Cross-references

- For person attributes (anything about a specific human), bounce
  to `contacts` (`contact_save`). The contact directory is the
  authoritative person record; memory should not duplicate it.
- For complex or evolving knowledge that won't fit in a key+value
  shape, bounce to `documents` for managed docs or workspace files
  for ad-hoc notes.
- For "what did we say about this in past conversations," bounce
  to `archive` — the search surface there is conversation history,
  not stored facts.
- For the *craft* of writing session working memory (tone, arc,
  texture rather than action notes), the
  [`working-memory.md`](working-memory.md) foundation talent loads
  on every turn already; this leaf carries only the operational
  read/write semantics.
