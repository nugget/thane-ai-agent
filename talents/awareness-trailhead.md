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

- Use `list_entity_subscriptions` to see what this loop is already carrying.
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
- Add `include` metadata flags when area, owning device, HA labels, or
  descriptions would make the subscribed state easier to interpret; use
  `visibility` when hidden/enabled salience matters, and read
  `visibility.context_role` as the quick default-vs-forensic hint. Area
  metadata can carry the HA floor/building hierarchy too.
- Use `remove_entity_subscription` when the work is done. Stale
  subscriptions are quiet clutter.
- If the work is a one-shot state check, use the currently visible HA or
  context tool directly instead of subscribing first.
- If the next move is delivery, escalation, or interruption, activate
  `notifications`.

For service loops, subscribing and later releasing entities should feel
reflexive. For single-shot turns, prefer the shortest path that answers
the request.
