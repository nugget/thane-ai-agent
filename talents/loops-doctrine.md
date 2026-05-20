---
kind: doctrine
tags: [loops]
---

# Loops Doctrine

The model-facing front door for "do work" is the `thane_*` family.
Pick by lifecycle:

- `thane_now(task, ...)` — synchronous answer right now. Blocks the
  current turn until the delegate completes; result returns inline.
  Use for bounded investigation, summarization, controlled tool use
  where the answer is needed in this turn.
- `thane_assign(task, ...)` — async one-shot. The delegate runs in
  the background and reports back through the current conversation or
  channel when complete. Use when the work is long enough that the
  caller should move on rather than block.
- `thane_curate(intent, cadence, output)` — recurring service loop
  that maintains a managed document over time. Scaffolds the document
  with frontmatter recording loop ownership, derives sleep parameters
  from a cadence string ("hourly", "daily", "every 30 minutes",
  "30m", "1h"), and launches in one round-trip. Two output modes:
  `journal` appends a dated entry each cycle; `maintain` rewrites the
  body each cycle. Future versions will accept a directory ref for
  tree-shaped collections.

External wakes to running loops are infrastructural, not tool-shaped:
producer subsystems (feed pollers, forge subscriptions, channel
ingestion) dispatch structured event envelopes over the message bus to
the loop named in their subscription's `wake_loop`. From inside a
running service loop, use `set_next_sleep` to shorten its own cycle
when it has learned something time-sensitive. To route a concern up to
the core/owner loop, use `request_core_attention`.

## Two paths for "this loop cares about a stream of things"

There are two distinct mechanisms for binding a loop to a stream of
external information. Pick the one that matches the cadence and
event-shape of the source — they coexist on the same loop without
conflict.

**Ambient context injection (entity subscriptions).** Use for sources
that update frequently and where each tick is *not* iteration-worthy
on its own. The loop sees current values automatically on every
scheduled iteration; no wake is triggered. Set up at thane_curate
create time via the `entities` parameter, or adjust in flight with
`loop_update_entity_subscriptions` (by name from outside) or
`watch_entity` / `unwatch_entity` (from inside the loop itself).
Scope: per-loop, behind the scenes via an internal `focus_tag`. Good
fit: HA sensor values, weather state, presence — high-frequency
ambient state where "next iteration" is soon enough.

**Event-source wakes (`wake_loop` on a subscription).** Use for
sources where each event is genuinely iteration-worthy and worth an
immediate wake. The producer subsystem (forge subscription, media
feed) dispatches a structured envelope with the events as payload,
which wakes the target loop now if sleeping or queues for its next
iteration if processing. Set up with the source-specific follow tool
(`forge_repo_follow`, `media_follow`) and a `wake_loop` target. Good
fit: forge releases, RSS feed entries, low-cadence
something-just-happened signals where waiting for the next scheduled
iteration would be too slow.

Same loop, both mechanisms: a `thane_curate` loop watching HVAC
entities (ambient) can also receive forge release events for the
project that owns its scripts (event-source). The watch set keeps the
prompt informed every iteration; the forge wake fires an iteration
the moment a release lands.

The lower-level definition and runtime tools remain available for
inspection, control, and unusual launch shapes (event-driven,
mqtt-wake-only, multi-stage, supervisor-randomized metacog patterns, or
anything where the canonical family doesn't fit).

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

`spawn_loop` is for ad hoc work that should start immediately without
first becoming part of the persistent loop-definition registry. Reach
for it when the loop is temporary, experimental, or tightly tied to the
current moment.

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
views. Linting matters most when:

- the task text mentions a cadence
- the loop should use its own tagged tools directly
- fixed cadence matters more than jitter

Tagged service loops often want `profile.delegation_gating: "disabled"`
so the loop can use its own domain tools directly instead of falling
back into delegation-first orchestration.

Loop authoring is rare and high leverage. When you need it, copy a
known-good pattern from `loops-examples` and adapt it minimally instead
of rebuilding the launch shape from memory.
