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
  should auto-load while the work continues.
- Use `remove_entity_subscription` when the work is done. Stale
  subscriptions are quiet clutter.
- If the work is a one-shot state check, use the currently visible HA or
  context tool directly instead of subscribing first.
- If the next move is delivery, escalation, or interruption, activate
  `notifications`.

For service loops, subscribing and later releasing entities should feel
reflexive. For single-shot turns, prefer the shortest path that answers
the request.
