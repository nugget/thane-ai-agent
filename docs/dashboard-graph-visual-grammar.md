# Dashboard Graph Visual Grammar

The runtime graph is the primary operating surface for Thane.

This document captures the semantic channels we want to use for graph nodes so
the UI can grow richer without turning noisy or inconsistent.

## Principles

- Keep the graph alive and expressive.
- Use circles for movable entities. The central runtime anchor may remain a
  square exception.
- Avoid spending multiple meanings on the same visual channel.
- Prefer light decoration over heavy badges.
- Animate only active truth: work, sleep countdowns, alerts, delivery.

## Node Anatomy

- Core fill: entity family.
  Loop, delegate, channel, metacognitive, scheduled, and future resource or
  definition nodes should each live in a stable tone family.

- Radius: capacity tier.
  Radius should reflect weighted context-window tiers, not raw proportional
  scaling. Fallback heuristics may use model size estimates when context is not
  yet known.

- Primary ring: runtime state.
  Sleeping, waiting, processing, supervisor, degraded, stopped, and errored
  states should read from the outer ring first.

- Halo: emphasis and role.
  Selection, supervisor emphasis, and other high-importance roles belong in
  halos, not in the core fill.

- Rim badge/iconography: compact secondary truth.
  Small ring-attached badges are appropriate for capacity labels, delivery
  targets, trust indicators, and later policy/binding markers.

- Underglow: inherited trust or channel context.
  This is the right place for soft trust-zone cues.

- Motion: transient live activity.
  Pulses, flashes, shimmers, and directional edge motion should only happen
  when a real runtime change is occurring.

## Current Direction

The current implementation slice moves toward:

- category-driven core fills
- state-driven outer rings
- context-tier sizing
- a compact rim badge for capacity labels
- preserved supervisor halo and trust underglow

This is an intentional shift away from shape-heavy encoding and toward a more
durable circular node grammar that behaves well under force animation.
