---
kind: trailhead
tags: [awareness]
teaser: "Open when live entities or room context should stay present across turns."
next_tags: [ha, home, notifications]
---

# Awareness Trailhead

Awareness is for sustained attention. Use it when the same entity,
device, person, room, or live provider will matter across more than one
turn.

The working tool is the subscription: **loop-owned attention** on an
entity, declared once, feeding whatever the attention is for. Every
subscription answers three questions — decide all three deliberately:

**1. Who carries it?** Ownership is the `owner` parameter on
`add_entity_subscription` / `remove_entity_subscription`. Omit it (or
say `core`) for your own always-visible field of view — core is the
root container, and every context is core's context, so its
subscriptions render everywhere. Name a loop to put the entry on that
loop's spec instead, following its lifecycle; from inside a loop's own
turn, `watch_entity` / `unwatch_entity` do the same with no name.
Container-owned entries cascade to descendant loops unless marked
`self_only`. Rows owned by `system` (the person-presence ingestion
floor, from `person.track`) are read-only here.

**2. What does it feed?** Three feeds compose on one declaration:

- **Render** (the default): live state in the entity's block each
  turn. Add `history` windows for numeric trends ("what has it been
  doing"), or `transitions` / `transitions_window_seconds` for the
  recent change log as `{from, to, ago}` ("what just happened" —
  survives the shared recent-changes window's churn). The shared
  window still answers "what's been happening around the house."
- **Wake**: `wake: true` when the owning loop should *act* on change,
  not just see it next iteration. The loop is awakened with a
  `{entity, from, to, ago}` event — debounced and coalesced
  (`wake_debounce_seconds`; a loop's cadence follows its twitchiest
  wake subscription), so a chattering sensor becomes one wake carrying
  the latest change, never a wakestorm. Wake needs an executing owner
  loop — core is a container and cannot be woken. Boundary: this
  covers "wake me when this entity changes"; compound conditions, zone
  dwell, and template triggers belong to the HA-automation→MQTT
  pipeline (`mqtt_wake_add`) — both deliver through the same queue, so
  pick by where the condition logic lives.
- **Capture** comes for free: declaring `transitions` or `wake`
  automatically feeds the entity into state-change capture — you never
  need `mode` for these. The only remaining use for `mode: ingest` is
  capture WITHOUT any render or wake: the entity's changes appear in
  the shared recent-changes window while spending no per-turn context.

**3. What does it cost?** `requires_tag` gates rendering on a
capability tag — the macro set: one tag activation surfaces a
subject's tagged documents and its related entities together, and
deactivation drops both (render-only; capture and wake never follow
tag state). `ttl_seconds` auto-expires a bounded watch. Expansions and
logs are capped and advertise truncation. And when the work is done,
`remove_entity_subscription` — stale subscriptions are quiet clutter.

Mechanics that apply across all of the above:

- The `entity_id` accepts a concrete id, a glob (`binary_sensor.*door*`,
  `*_temperature`), or an organizational target — `area:<area_id>`,
  `label:<label_id>`, `floor:<floor_id>`. Prefer the organizational
  form when the intent is "watch the office," not "watch these specific
  sensors": membership re-resolves from the registry every turn, so it
  follows the home as devices move. Subscribing to a glob or target
  reports how many entities it matches right now, with a sample — a
  zero-member expansion almost always means a typo'd id; fix it before
  moving on. Registry targets are render-only (they cannot feed
  capture or wake). Use `ha_registry_search` to find ids.
- Add `include` metadata flags when area, owning device, HA labels, or
  descriptions would make the state easier to interpret; use
  `visibility` when hidden/enabled salience matters, and read
  `visibility.context_role` as the quick default-vs-forensic hint.
- Use `list_entity_subscriptions` to see the whole registry — every
  row carries its `owner` (`core`, a loop name, or `system`), its
  feeds, and its gates.
- If the work is a one-shot state check, use the currently visible HA
  or context tool directly instead of subscribing first.
- If the next move is delivery, escalation, or interruption, activate
  `notifications`.

For service loops, subscribing and later releasing entities should feel
reflexive. For single-shot turns, prefer the shortest path that answers
the request.
