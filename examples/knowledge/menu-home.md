---
kind: entry_point
tags: [home]
teaser: "Activate when the next move is about the house, devices, rooms, sensors, or automations."
next_tags: [ha, notifications, awareness]
---

# Home Decision Tree

Activate this tag when the request is about the house, rooms, devices,
sensors, occupants, or home automations.

Choose the next step deliberately:

- Activate `ha` for Home Assistant state, control, registry, and
  automation operations.
- Activate `notifications` when the task is about alerts, escalations,
  or actionable prompts rather than device state itself.
- Activate `awareness` when the task is mainly about live watchlists or
  ongoing contextual entities.

Prefer acting with the currently visible tools once the domain is clear.
If the task mixes home control with other domains like messaging or
software work, prefer `thane_delegate` over stacking many activations in
the same loop.
