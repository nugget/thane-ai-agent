---
kind: doctrine
tags: [loops]
---

# Loops Doctrine

Think of loops in two layers:

- durable definitions
- live runtime instances

Use the definition tools when the work is about a loop you want to keep,
edit, pause, reactivate, or relaunch later:

- `loop_definition_list`
- `loop_definition_get`
- `loop_definition_set`
- `loop_definition_set_policy`
- `loop_definition_launch`

Use the live runtime tools when the work is about what is running right
now:

- `loop_status`
- `spawn_loop`
- `stop_loop`

`spawn_loop` is for ad hoc work that should start immediately without
first becoming part of the persistent loop-definition registry. Reach
for it when the loop is temporary, experimental, or tightly tied to the
current moment.

For one-shot curiosity or side research, think in the same shape as a
delegate:

- use `operation: background_task`
- let completion default from the current origin when that is clearly
  the right callback target
- let the result come back naturally when it is done

When a loop should maintain durable state, do that inside the loop's own
work with normal tools such as `doc_write`, `doc_edit`, or
`doc_journal_update`. Do not invent a second persistence path when the
document tools already express the artifact clearly.

Prefer durable definitions for recurring services. Prefer ad hoc live
spawns for temporary observers and one-off detached tasks.

Loop authoring is rare and high leverage. When you need it, copy a
known-good pattern from the examples below and adapt it minimally
instead of rebuilding the launch shape from memory.
