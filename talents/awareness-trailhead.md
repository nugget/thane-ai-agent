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

Choose the next move deliberately:

- Use `list_entity_subscriptions` to see the whole subscription
  registry — every row carries its `owner`: `core` is the
  always-visible tier (core is the root container, and every context
  is core's context — this is your own field of view), a loop name is
  that loop's own watch set, and `system` marks runtime-seeded rows
  like the person-presence ingestion floor (configured via
  `person.track`, read-only here).
- Use `add_entity_subscription` when a room, device, person, or live state
  should auto-load while the work continues. The `entity_id` accepts more
  than a single entity: a glob (`binary_sensor.*door*`, `*_temperature`)
  to follow entities by name, or an organizational target —
  `area:<area_id>`, `label:<label_id>`, `floor:<floor_id>` (e.g.
  `area:office`). Prefer the organizational form when the intent is
  "watch the office," not "watch these specific sensors": its membership
  is re-resolved from the registry every turn, so it follows the home —
  move a device into the office and the `area:office` watch picks it up,
  no re-authoring. When you subscribe to a glob or organizational target,
  the result reports how many entities it matches right now, with a
  sample — and flags a zero-member expansion, which almost always means a
  typo'd id or an empty group. Read that back: if `area:office` was meant
  to catch three sensors and matches zero, fix the id before moving on.
  All expansions are capped per turn and report truncation when they
  overflow; scope to a smaller area/label/floor rather than watching the
  whole house. Use `ha_registry_search` to find area/label/floor IDs.
- Pass `mode` to choose what a subscription feeds: `render` (default)
  injects live state each turn; `ingest` feeds the recent-state-changes
  window only — the entity's transitions appear there without spending
  per-turn context on its full state; `both` does both. Ingest is the
  right shape for "I want to notice when this changes" without carrying
  it every turn. Ingest accepts entity ids and globs (not
  area/label/floor targets), and the ingest registry is capped.
- Pass `transitions` (and/or `transitions_window_seconds`) when the
  entity's recent *changes* matter, not just its current state: the
  rendered block gains its last-n transitions as `{from, to, ago}`,
  and capture follows automatically — you never need `mode` for this.
  Pick by question shape: the transition log answers "what just
  happened to THIS entity" (survives the shared window's churn),
  `history` summaries answer "what has it been doing" (numeric trends
  over windows), and the shared recent-state-changes window answers
  "what's been happening around the house."
- Pass `wake: true` when a loop should *act* on change, not just see
  it next iteration: the owning loop is awakened with a
  `{entity, from, to, ago}` event — debounced and coalesced, so a
  chattering sensor becomes one wake carrying the latest change,
  never a wakestorm. `wake_debounce_seconds` sets how long changes
  coalesce (a loop's cadence follows its twitchiest wake
  subscription); capture follows automatically. Wake needs an owning
  loop (`owner`, or `watch_entity` from inside one). Boundary: this
  covers simple native triggers — "wake me when this entity changes."
  Compound conditions, zone dwell, and template triggers belong to
  the HA-automation→MQTT pipeline (`mqtt_wake_add` + an HA
  automation); both deliver through the same queue, so pick by where
  the condition logic lives, not by delivery mechanics.
- Add `include` metadata flags when area, owning device, HA labels, or
  descriptions would make the subscribed state easier to interpret; use
  `visibility` when hidden/enabled salience matters, and read
  `visibility.context_role` as the quick default-vs-forensic hint. Area
  metadata can carry the HA floor/building hierarchy too.
- Pass `owner` to subscribe on behalf of a loop: the entry lands on
  that loop's spec and follows its lifecycle (containers cascade
  subscriptions to descendants unless the entry sets `self_only`).
  From inside a loop's own turn, `watch_entity` does the same without
  naming the loop.
- Pass `requires_tag` to build a macro set: the subscription renders
  only while that capability tag is active, so activating one tag
  surfaces a subject's tagged documents and its related entities
  together — and deactivating it drops both. The gate is render-only
  (it cannot combine with `mode: ingest`/`both`) and is the exception,
  not the default: an ungated subscription is the smooth path when the
  entity should simply always be there.
- Use `remove_entity_subscription` when the work is done. Stale
  subscriptions are quiet clutter. The same `owner` addressing
  applies.
- If the work is a one-shot state check, use the currently visible HA or
  context tool directly instead of subscribing first.
- If the next move is delivery, escalation, or interruption, activate
  `notifications`.

For service loops, subscribing and later releasing entities should feel
reflexive. For single-shot turns, prefer the shortest path that answers
the request.
