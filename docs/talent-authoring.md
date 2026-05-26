# Authoring Talents

Read this before writing or restructuring a talent. The audience for a
talent is the model — past-self writing for present-self about how to
move through a domain. The audience for *this* doc is the human
contributor authoring those talents. The
[talents/README.md](../talents/README.md) is the quick-reference
companion (file naming, frontmatter shape, what kinds exist, regression
tests). This page is about craft: how to mark a good trail.

The mental model is trail marking. The model has access to a sprawling
tool surface — somewhere between 130 and 200 native tools depending on
configuration, plus MCP additions. Talents are the trail blazes that
say "this way for that kind of work" without trying to be the whole
map. A good talent lowers the cost of finding the right tool in the
right shape for the task at hand. A bad talent is a buffet — every
option listed equally, no opinion taken, no decision easier.

## The four shapes and what each is for

### Foundation prose (always on)

`foundation.md`, `presence.md`, `awareness.md`, `communication.md`,
`delegation.md`, `working-memory.md`. No frontmatter, no tags. The
content is identity — how the model should *be* across every turn,
regardless of what it's doing. Concrete-tool guidance does not belong
here; the right home for "use email_read when the message is worth
reading" is the email-tagged doctrine talent, not foundation prose.

Add a foundation talent rarely. Every line earns an always-on slot in
the model's prompt forever. The bar is "this is who the model is,"
not "this is useful sometimes."

### Trailheads (decision-tree roots)

`<tag>-trailhead.md`. The first thing the model reads when a capability
tag activates. A trailhead asks the framing question for the domain
and points at where to look next. It does not catalog the domain. See
the next section for the canonical shape.

### Doctrine (one-domain posture)

`<tag>.md` (when there's one doctrine file per tag) or
`<tag>-doctrine.md` (when another talent shares the tag). The voice
that explains how this domain rewards being worked. Tool routing belongs
here when the routing is *judgment*, not menu navigation. The
`loops.md`, `forge.md`, and `notifications.md` talents are the current
examples of doctrine-shaped prose.

### Examples (concrete patterns)

`<tag>-examples.md`. Usually multi-node. The decision-tree leaves the
trailhead points at, each carrying concrete JSON the model can adapt
minimally. `loops-examples.md` is the canonical implementation — eight
nodes, three depths, each leaf a real launch pattern.

## The decision-trail pattern

The shape that works:

```
trailhead root  →  decision frame + per-leaf teasers
   ├── leaf-A   →  concrete shape + JSON template
   ├── leaf-B   →  concrete shape + JSON template
   └── leaf-C   →  (if needed) further fork to depth-2 leaves
```

Three properties make a trail useful:

**The root asks the question, not the answers.** Don't open a trailhead
with a list of tools. Open with the choice the model is actually
making. "Choose by lifecycle" (loops). "First move: look at what's
already running" (loops examples). The model arrives at the trailhead
because a tag activated; it doesn't need the trailhead to re-establish
why the domain matters.

**Each branch carries its own teaser.** The `teaser:` frontmatter field
is what shows up at decision time. It's the only thing the model sees
about a leaf before activating it. A good teaser is one sentence,
trigger-condition oriented, specific enough that the model can
*disqualify* it as fast as it can qualify. "Recurring service loops
that maintain a managed document over time" tells the model both "yes
when X" and "no when not-X."

**Leaves are concrete.** The success criterion for a leaf is: the model
can adapt the leaf's content into a real tool call with minimal
reshaping. For `loops_examples_curate_dashboard`, that means a full
`thane_curate` JSON literal with realistic entity IDs, a real sleep
envelope, and the `output` block already filled out. The model swaps
the topic-specific details and calls. It does not have to translate
prose into structure.

### Worked example: the `loops-examples.md` root

```yaml
---
name: loops_examples
tags: [loops_examples]
kind: trailhead
teaser: "Open when about to launch any loop-shaped work. Walks you to the right thane_* call."
next_tags: [loops_examples_curate, loops_examples_now, loops_examples_assign, loops_examples_advanced]
---

# Loops Examples

Loops are how Thane runs concurrent attention. This is the entry point
for picking the right shape of loop for the work in front of you.

## First move: look at what's already running
[short paragraph — orientation, not enumeration]

## Choose the shape of work
Activate the next tag based on what shape this work has:
- **Recurring service work that owns a document** — activate
  `loops_examples_curate`. The default and most-used path. [...]
- **Sync foreground work that must finish before you reply** —
  activate `loops_examples_now`. [...]
[...two more branches...]
```

Read what that root *doesn't* do:
- It doesn't tell the model what `thane_curate` is. The leaf does.
- It doesn't enumerate all loop tools. The leaves do.
- It doesn't have a "Quick Reference" section. The leaves cover the
  concrete shapes.
- It doesn't tell the model "if confused, ask the operator." The
  leaves resolve confusion.

The root's whole job is to make the *choice* legible. Everything else
is leaf work.

### Sub-trailheads

When a leaf is itself a multi-way choice, make it a sub-trailhead.
`loops_examples_curate` is one — picking `thane_curate` doesn't
finish the decision; there are still three sub-shapes (dashboard,
journal, circle). The sub-trailhead asks the next question:

> 1. Does each cycle replace the document or append to it?
> 2. Does the loop need to escalate decisions to you, or accept new
>    focus when you adjust its scope?

Same rules apply: ask, don't catalog. Teasers on each downstream leaf.
Concrete JSON when the leaf is the actual destination.

Don't sub-trailhead pre-emptively. If a leaf carries one concrete
pattern, it's a leaf. If it carries three patterns with a real choice
between them, it's a sub-trailhead with three children. The signal: you
find yourself writing "do this OR do that OR do the other" inside one
leaf's prose.

## When to use multi-node, when to keep it flat

Multi-node is right when:
- The content is a *choice between concrete patterns* that don't
  overlap (loops, documents, forge mutation workflows).
- The model benefits from seeing per-leaf teasers at decision time
  rather than reading the whole file end-to-end.
- The patterns are different enough that putting them in one file
  would force the model to read past two-thirds of irrelevant content
  to find its match.

Flat prose is right when:
- The content is posture or identity, not pattern selection
  (`foundation.md`, `notifications.md`, `delegation.md`).
- The judgment is "always do X" or "never do Y," not a multi-way
  branch.
- The file is short enough that the model can read end-to-end without
  cost (under ~50 lines).

Signal that you've written cafeteria-shaped prose and should split:
you have a list of more than 5 bullets that each start with "use X
when Y" or "use X if Z." That's a decision tree pretending to be a
list. Convert.

Signal that multi-node is overkill: you have two patterns and they're
sequential ("first do A, then do B") rather than alternative
("either A or B"). Flat prose with a numbered list is the right
shape.

## Teaser-writing craft

The `teaser:` field is high-stakes copy. It's what the model sees when
the parent menu surfaces this branch — often the only thing it sees
before deciding whether to activate.

**Lead with the trigger condition.** "Open for code, repos, PRs,
issues, releases, or implementation work" works because it gives the
model the matching test up front. "Comprehensive code collaboration
tooling" doesn't, because the model doesn't ask "is this
comprehensive?" — it asks "does this fit what I'm doing?"

**Be specific enough to disqualify.** A good teaser tells the model
when *not* to activate the branch. "Recurring service loops that
maintain a managed document over time" rules out one-shot work, async
work, and supervisor work in one line.

**Under ~100 characters.** The teaser is read fast. Long teasers blur.
Compare:

> ✅ `"Open when live entities or room context should stay present
> across turns."`

> ❌ `"Activate this tag when working with home automation entities
> or room context information that you want to keep visible throughout
> the conversation, so you can reference current state without
> requerying."`

**Voice = imperative or descriptive, not narrative.** "Open for X."
"Walks you to Y." Not "This is where you go when..." or "If you find
yourself wanting Z..."

**Write from the model's POV.** The model is reading these to pick.
You're writing to its decision moment, not introducing the file's
contents.

## Concrete JSON in leaves

The success criterion for an examples leaf is: a model reading the
leaf can produce a working tool call by adapting the displayed JSON.
Not by translating prose into JSON. Not by combining hints from three
paragraphs. By adapting the literal shape.

Bad leaf content:

> Use `thane_curate` with a sensible sleep envelope and the right
> output mode for the use case. Include any entities the loop should
> watch. Pick tags that fit the work.

Good leaf content:

````
```json
{
  "name": "server_closet_guardian",
  "intent": "Watch the server-closet environment and equipment health...",
  "sleep_min": "10m",
  "sleep_max": "30m",
  "entities": [
    {"entity_id": "sensor.server_closet_temperature"},
    {"entity_id": "sensor.ups_hor_rack_status"}
  ],
  "output": {
    "document": "kb:dashboards/server-closet.md",
    "mode": "maintain",
    "title": "Server Closet Guardian"
  },
  "tags": ["home", "ha", "awareness"]
}
```
````

The model swaps `server_closet_guardian` for whatever the actual
loop should be named, edits the intent, picks the right entities,
picks the right sleep envelope, picks the right output document. It
doesn't have to invent the *shape* of any of those fields.

**Use realistic field values.** `"sleep_min": "10m"` beats
`"sleep_min": "<duration>"`. Real values teach the model what
reasonable looks like; placeholders teach the model that the schema is
fuzzy and improvisation is fine.

**Don't hide important fields.** If `delegation_gating: "disabled"`
matters for tagged service loops (it does), put it in the example
JSON. The leaf is the model's reference; leaving a load-bearing field
out of the example invites it to be omitted in the tool call too.

## Anti-patterns

### Cafeteria-style talents

> - Use `doc_read` when you need to read a document
> - Use `doc_search` when you need to search documents
> - Use `doc_edit` when you need to edit a document
> - Use `doc_browse` when you need to browse documents
> ... (12 more bullets)

This *looks* structured. It isn't. The model gets a list, has to
match the description to its task, and the descriptions are mostly
restatements of the tool names. No judgment is encoded. Convert to a
decision tree if the choice has real branches, or to doctrine prose if
the choice is judgment (`documents.md` is being restructured this way
in PR-D).

### Restating `next_tags` in the body

> Activate `forge` for code repository work. Activate `files` for
> workspace file work. Activate `web` for outside sources.

The model already sees these in the menu — the `next_tags`
frontmatter is what surfaces them. The trailhead body should explain
*how to choose between them*, not restate them. Use the body for the
disambiguating question ("the truth probably lives where?") instead.

### Ghost tool names

`watch_entity`, `email_compose`, `unwatch_entity` — none of these
exist. They look like they should, which is exactly why they get
written. The `TestRepoTalentToolReferences` regression test catches
the obvious shape; the less obvious shape is the model wanting a
verb-noun tool that doesn't exist and the talent confirming the
hallucination. When you reach for a tool name while writing a talent,
verify it's in
[`internal/model/toolcatalog/catalog.go`](../internal/model/toolcatalog/catalog.go)
or run `grep "Name:" internal/tools/*.go` to confirm.

### Wrong-data-store substitution

This bit PR-A: `add_entity_subscription` and
`remove_entity_subscription` are real tools, but they mutate the
*conversation-wide* always-visible subscription set. The loop-scoped
subscription set (the one a `thane_curate` loop creates) is mutated
by `update_entity_subscriptions`. The talent said "adjust the watch
set via add/remove" when the watch set in question lived in a
different store. The regression test can't catch this because both
names ARE real tools. The author has to.

When you cite a tool in a talent that mutates state, verify *which
store* it mutates and whether that's the store the talent's reader
will be working with.

### Trailheads that are doctrine in disguise

If a "trailhead" body has no `next_tags` and no fork — just one
paragraph of posture — it's not a trailhead, it's doctrine that
should drop the `kind: trailhead` declaration. The discriminator: a
trailhead points somewhere else. Doctrine stands alone.

## Verification

Two regression tests in
[`internal/model/talents/repo_corpus_test.go`](../internal/model/talents/repo_corpus_test.go)
guard the corpus:

- `TestRepoTrailheadNextTagsResolve` — every trailhead's `next_tags`
  must resolve to either a built-in capability tag or a tag declared
  by another loaded talent. Catches typos, deprecated tag references,
  and broken multi-node chains.
- `TestRepoTalentToolReferences` — backticked snake_case tool
  references must match a registered tool, OR be in the
  `nonToolTokens` allowlist (which documents the explicit "this isn't
  a tool" decisions). Catches `email_compose`-class hallucinations.

Both run as part of `just ci`. If you legitimately need to reference
a non-tool snake_case identifier (config field name, frontmatter key,
etc.), add it to the allowlist with a comment explaining why. The
allowlist is short on purpose; every addition is a small architectural
decision.

The third safety net is `TestTalents_SkipsContributorDocs` in the
loader tests — it pins the rule that uppercase-leading markdown files
in the talents directory are contributor docs, not talents. Without
that filter, this very document — if it lived in `talents/` instead
of `docs/` — would silently inject into every model prompt forever.

## When you're stuck

Two reference talents to read when you're not sure what shape to use:

- [`talents/loops-examples.md`](../talents/loops-examples.md) — the
  canonical multi-node decision tree. Eight nodes, three depths,
  realistic JSON throughout. Read the root and one leaf before
  starting any new examples-shaped talent.
- [`talents/loops.md`](../talents/loops.md) — the canonical doctrine
  voice. Flat prose, no `next_tags`, posture-first.

If your draft doesn't feel like either, it's probably trying to do
both. Decide which one and split if needed.
