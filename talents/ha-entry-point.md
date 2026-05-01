---
kind: entry_point
tags: [ha]
teaser: "Open for Home Assistant state, control, registry, automation, and history."
next_tags: [awareness, notifications, operations, ha_admin]
---

# HA Entry Point

Home Assistant work is rarely truly single-shot. State drifts, devices
report, automations fire — your understanding goes stale fast. Match
the shape of the work to the right surface:

- For one-off state checks, service calls, or registry lookups, the
  tools loaded with `ha` are enough on their own.
- For sustained attention — a service loop watching a room, a guardian
  loop tracking a device, any conversation that wants entity state to
  stay current between turns — activate `awareness` and subscribe the
  entities you care about. Adding and dropping subscriptions should be
  reflexive: take what you need, release it when the work is done.
- If the next move is delivery, alerts, or interruption rather than
  state, activate `notifications`.
- If the operational issue is wake signals, MQTT plumbing, or scheduler
  policy rather than HA state, rebound through `operations`.

## Verifying device control

`call_service` does not validate entity IDs — a typo or stale ID
returns success and silently does nothing. Treat any control action as
a three-step move:

1. `find_entity` (or the loaded MCP entity-search equivalent) to confirm
   the entity actually exists and grab its real ID.
2. `call_service` (or the MCP equivalent) to perform the action.
3. `get_state` afterwards to verify the new value really took.

Never trust `call_service` success alone — for anything that matters,
read the state back.
