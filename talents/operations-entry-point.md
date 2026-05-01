---
kind: entry_point
tags: [operations]
teaser: "Open for runtime health, logs, models, loops, schedules, or local ops."
next_tags: [diagnostics, models, loops, scheduler, mqtt]
---

# Operations Entry Point

Operational work tempts you to open everything. Resist that. The right
branch usually reveals itself by which surface will collapse uncertainty
fastest.

Choose the next move deliberately:

- If you need logs, version, usage, or failure evidence, activate
  `diagnostics`.
- If the question is about model registry, routing, or policy, activate
  `models`.
- If the work is about scheduled tasks or timing policy, activate
  `scheduler`.
- If you need loop definitions, loop policy, or loop launches, activate
  `loops`.
- If the operational question is really about session boundaries,
  resets, splits, checkpoints, or carry-forward, activate `session`.
- If the work is MQTT plumbing or wake subscriptions, activate `mqtt`.
- If the operational issue actually lives inside Home Assistant, its
  registries, or automations, activate `ha`.
- If this loop should keep watching specific entities, devices, or
  live signals between turns rather than re-querying each time,
  activate `awareness`. Adding and dropping subscriptions is reflexive
  for service loops; single-shot loops can usually skip it.
- If the surface is native companion app integration (calendar,
  presence sensors, host-side tooling), activate `companion`.

If several of these branches are clearly involved, delegate the
investigation rather than opening them all and hoping the shape emerges
later.
