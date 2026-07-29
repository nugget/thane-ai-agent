---
kind: trailhead
tags: [loops]
teaser: "Open when work should recur, run detached, or outlive this turn."
next_tags: [loops_examples, documents, awareness]
---

# Loops Trailhead

Everything here runs on the same loop machinery — the operations differ
in how often they run and in what they leave behind. Answer two
questions in order: the first picks how often, the second decides
whether anything needs to be stored.

**1. Does it run once, or keep running?** One-shot work is a loop that
executes and finishes. `thane_now` runs it inline and returns the result
to this turn, so reach for it when you cannot answer without the work.
`thane_assign` runs it detached and reports back later, so reach for it
when the turn should not wait. Neither leaves a reusable definition, and
neither is worth a definition: they do the work and end.

Work that recurs on its own schedule, or waits quiescent for a trigger,
is the rest of this file.

**2. Should it still exist tomorrow?** A durable loop leaves a stored
definition you can inspect, edit, pause, reactivate, and relaunch. An ad
hoc one does not.

- **Durable** — `loop_definition_set` writes the spec, then
  `loop_definition_launch` starts it. Lint first with
  `loop_definition_lint`: it takes the same spec and catches authoring
  mistakes before anything persists. This is the path for any loop that
  curates an understanding over time.
- **Ad hoc** — `spawn_loop` launches immediately from a full spec and
  persists nothing. Right for a temporary service that should not join
  the durable registry, and for one-shot work needing spec-level control
  `thane_assign` does not expose. Wrong for anything that owns a
  document: the loop ends, the document remains, and its ownership
  frontmatter points at a loop that no longer exists.

`thane_loop_create` is a shorter front door to the durable path. It
takes one output document, can declare its facets, and always gives the
loop a private working-notes document alongside. Both documents are
scaffolded before launch — a faceted one with the exact section
skeleton its publish tool fills — and a document that already exists
is preserved, never re-scaffolded. Pass `dry_run: true` to preview the
derived spec without building anything. Reach for
`loop_definition_set` when a loop needs more than one published document
or a field the front door does not expose.

## Where to go next

- The doctrine that follows this file covers pacing, output ownership,
  faceted publishing, and working notes. Read it before authoring a spec.
- `loops_examples` opens worked specs — a curating loop with facets, a
  bi-directional service, a delegated one-shot. Start there rather than
  composing a spec from the schema alone.
- `documents` covers the managed roots a loop's outputs live in, and the
  policy that decides whether an output is searchable, injectable, or
  private.
- `awareness` covers entity subscriptions, which is how a loop watches
  something and how an event-driven loop gets woken at all.
