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

For recurring document work, prefer `thane_curate`. Its `cadence`
schedules the service, its `output` declares the state owner, and the
running loop writes through generated output tools such as
`replace_output_*` or `append_output_*`. If a maintained output is
marked `truncated` in Declared Durable Outputs, read the full document
before replacing it.

Choose stream wiring by attention cost:

- Use entity subscriptions for ambient state the loop should see on its
  normal turns. `thane_curate.entities` creates the initial watch set;
  `loop_update_entity_subscriptions`, `watch_entity`, and
  `unwatch_entity` adjust it later.
- Use event-source `wake_loop` targets when each event deserves an
  immediate iteration. Producer tools such as `forge_repo_follow` and
  `media_follow` own those subscriptions.

Natural-language timing inside a task does not schedule a service loop.
Use `thane_curate.cadence` or explicit service-loop sleep fields. Lint
hand-authored durable definitions before saving them, especially when
cadence, jitter, or direct domain-tool access matters. Tagged service
loops often want `profile.delegation_gating: "disabled"`.

When you need concrete JSON launch patterns, activate `loops_examples`
and adapt the closest recipe minimally.
