---
kind: doctrine
tags: [loops]
---

# Loops Doctrine

Think of loops in two layers:

- durable definitions
- live runtime instances

For the common case of "keep watching X over time and write what you
learn into a document," reach for `service_journal` first. It is an
intent-shaped shim that scaffolds the output document with frontmatter
recording loop ownership, derives sleep_min/max/jitter from a cadence
string ("hourly", "daily", "every 30 minutes", "30m", "1h"), and
launches the loop in one round-trip — replacing what would otherwise be
a multi-tool dance through `doc_write` + `loop_definition_set` +
`loop_definition_launch`. Two output modes: `journal` appends a dated
entry each cycle (research notes, decision logs); `maintain` rewrites
the body each cycle (dashboards, current-state snapshots).

Drop to the lower-level definition and runtime tools below when the
launch shape is genuinely unusual (event-driven, mqtt-wake-only, no
durable output, multi-stage), when you are inspecting or editing
existing loops, or when you need to control a loop that is already
running.

Use the definition tools when the work is about a loop you want to keep,
edit, pause, reactivate, or relaunch later:

- `loop_definition_lint`
- `loop_definition_list`
- `loop_definition_get`
- `loop_definition_set`
- `loop_definition_set_policy`
- `loop_definition_launch`

Use the live runtime tools when the work is about what is running right
now:

- `loop_status`
- `set_next_sleep`
- `spawn_loop`
- `stop_loop`
- `notify_loop`

`spawn_loop` is for ad hoc work that should start immediately without
first becoming part of the persistent loop-definition registry. Reach
for it when the loop is temporary, experimental, or tightly tied to the
current moment.

`notify_loop` is for an already-running timer-driven loop that needs a
single tap on the shoulder:

- wake a sleeping loop immediately with one-shot context
- force the next iteration into supervisor mode when that is the real need
- do not use it as a persistence mechanism; the notification is only for the
  next iteration

`set_next_sleep` is for the loop that is already running right now:

- call it from inside a timer-driven service loop
- ask for the next wake duration in plain duration syntax like `15m`
- the runtime clamps the request to the loop's configured sleep bounds
- use it when the loop has learned something that should change its
  next attention cycle

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

Natural-language timing in `task` does not schedule a service loop.
Words like hourly, daily, or every night do nothing on their own. For
service loops, set `sleep_min`, `sleep_max`, `sleep_default`, and
`jitter` deliberately.

Before saving or replacing a durable service definition, use
`loop_definition_lint` or inspect the warnings returned by definition
views. This is especially important when:

- the task text mentions a cadence
- the loop should use its own tagged tools directly
- fixed cadence matters more than jitter

Tagged service loops often want `profile.delegation_gating: "disabled"`
so the loop can use its own domain tools directly instead of falling
back into delegation-first orchestration.

Loop authoring is rare and high leverage. When you need it, copy a
known-good pattern from the examples below and adapt it minimally
instead of rebuilding the launch shape from memory.
