---
kind: trailhead
tags: [loops]
teaser: "Open when work should recur, run detached, or outlive this turn."
next_tags: [loops_examples, documents, awareness]
---

# Loops Trailhead

A loop is work that outlives the turn that created it. Before reaching
for one, answer two questions in order — the first picks the door, the
second decides whether anything needs to be stored at all.

**1. Must it finish before you reply?** Then it is not a loop.
`thane_now` runs bounded work inline and returns its result to this
turn. `thane_assign` hands off one-shot background work that reports
back later. Neither leaves a reusable definition behind, and that is the
point: they do work and exit.

**2. Should it still exist tomorrow?** A durable loop leaves a stored
definition you can inspect, edit, pause, reactivate, and relaunch. An ad
hoc one does not.

- **Durable** — `loop_definition_set` writes the spec, then
  `loop_definition_launch` starts it. Lint first with
  `loop_definition_lint`: it takes the same spec and catches authoring
  mistakes before anything persists. This is the path for any loop that
  curates an understanding over time.
- **Ad hoc** — `spawn_loop` launches immediately from a full spec and
  persists nothing. Right for a temporary service or detached research
  that should not join the durable registry. Wrong for anything that
  owns a document: the loop ends, the document remains, and its
  ownership frontmatter points at a loop that no longer exists.

`thane_loop_create` is a shorter front door to the durable path. It
takes one output document and cannot declare tiers or working notes, so
reach for `loop_definition_set` whenever the loop curates something
others consult — which is most service loops worth writing.

## Where to go next

- The doctrine that follows this file covers pacing, output ownership,
  tiered publishing, and working notes. Read it before authoring a spec.
- `loops_examples` opens worked specs — a curating loop, a journal, a
  dashboard, a delegated one-shot. Start there rather than composing a
  spec from the schema alone.
- `documents` covers the managed roots a loop's outputs live in, and the
  policy that decides whether an output is searchable, injectable, or
  private.
- `awareness` covers entity subscriptions, which is how a loop watches
  something and how an event-driven loop gets woken at all.
