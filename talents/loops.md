---
kind: doctrine
tags: [loops]
---

# Loops Doctrine

Choose loop tools by lifecycle:

- `thane_now` for bounded work that must finish before you reply.
- `thane_assign` for one-shot background work that should report back
  later.
- `thane_curate` for recurring service work that owns a managed document
  over time.
- `spawn_loop` for ad hoc loop-shaped work that should start now without
  becoming a stored definition.
- loop definition tools for durable services you need to inspect, edit,
  pause, reactivate, or relaunch.

For recurring document work, prefer `thane_curate`. Its `sleep_min`
and `sleep_max` set the envelope the running loop self-paces inside,
its `output` declares the state owner, and the running loop writes
through generated output tools such as `replace_output_*` or
`append_output_*`. If a maintained output is marked `truncated` in
Declared Durable Outputs, read the full document before replacing it.

Choose stream wiring by attention cost:

- Use entity subscriptions for ambient state the loop should see on its
  normal turns. `thane_curate.entities` creates the initial watch set;
  `update_entity_subscriptions` adjusts it later — that's the
  loop-scoped store. Don't reach for `add_entity_subscription` /
  `remove_entity_subscription` here; those mutate the
  conversation-wide always-visible subscription set, a different
  store, and using them on a loop's scope mutates the wrong thing.
  Add `include` metadata flags (`area`, `device`, `labels`,
  `description`, `visibility`, or `all`) when the loop needs
  physical-world context beside the live state, including HA's
  floor/building hierarchy and hidden/enabled salience.
- Use event-source `wake_loop` targets when each event deserves an
  immediate iteration. Producer tools such as `forge_repo_follow` and
  `media_follow` own those subscriptions.

Treat running loops as bi-directional. A curate loop can pull you in
via `request_core_attention` when something deserves a decision; you
can push new focus down by adding entities to a running loop's watch
set with `update_entity_subscriptions`, or by pointing a producer's
`wake_loop` target at the loop. Inspect what is already running with
`loop_status` and `loop_definition_get` before launching a parallel
loop — a thriving loop is its own data-dense documentation and is
usually the right thing to extend.

**`request_core_attention` forces a supervisor turn** on your next
iteration — costlier than a normal wake. Reserve it for concerns
that genuinely warrant the extra capacity, not as a routine
notification channel.

Natural-language timing inside a task does not schedule a service loop.
Pick a sleep envelope (sleep_min, sleep_max) tight enough to catch what
matters and loose enough to cost nothing when quiet; the running loop
uses `set_next_sleep` to self-pace inside it. Lint hand-authored
durable definitions before saving them, especially when the envelope,
jitter, or direct domain-tool access matters. Tagged service loops
often want `profile.delegation_gating: "disabled"`.

## Composing the loop

A `thane_curate` spec separates `intent` (what this loop tracks and
why), `instructions` (steering text prepended to every iteration —
the spec's `Profile.Instructions`), and `output` (the document target
and mode). Get the boundaries right and the loop runs honestly;
muddle them and the loop drifts.

- **Document destinations belong in `output`, not in `intent`.**
  "Update kb:foo with the current state" is the wrong shape for the
  intent; the loop already knows where to write because `output:
  {document: "kb:foo", mode: "maintain"}` told it. Intent names
  *what to observe and why it matters*, not where to write. Document
  management is a framework concern; the intent shouldn't restate it.

- **`instructions` tone shapes the loop's posture.** Earnest,
  mission-focused, concrete about what to look for. Skip distracting
  meta-commentary ("this is our first attempt", "let's see if this
  works") — it leaks into the loop's voice and erodes the focused
  attention the loop is meant to embody. Treat instructions as
  setting an intention, not as commentary about the experiment.

- **Reach for a loop when the source is messy.** Scripts win on
  structured feeds — the parser is short, the failure modes are
  finite. Loops win on human-edited sources with inconsistent
  formatting, drifting layouts, or judgment calls about what changed.
  If you find yourself writing brittle parsing logic to coax a
  script through unstructured input, that's the signal — a loop
  with interpretive instructions handles it more durably.

When you need concrete JSON launch patterns, activate `loops_examples`
and adapt the closest recipe minimally.
