---
tags: [loops]
---

# Loop tagging

When you launch a loop, the `tags` array decides what tools its
iterations can reach. Get the tag set right and the loop has exactly
what it needs; get it wrong and you've either starved the loop (no
relevant tools) or distracted it with menus it doesn't need. Tagging
is the single most consequential field on a loop launch after the
sleep envelope.

## "Always" means two different things

There are two always-X concepts and they're often confused:

- **Always-active tags** are capability tags the operator has marked
  always-active in their site config. They load into every
  conversation and every loop iteration by default, *in addition to*
  whatever the loop's own `tags` array specifies. Site-specific —
  one deployment's always-active set isn't another's. Inspect with
  `list_loaded_capabilities` (entries with `always_active: true`).

- **Always-available tools** are a small set of tools that survive
  capability-tag filtering regardless of the loop's tag scope:
  `activate_capability`, `deactivate_capability`, `reset_capabilities`,
  `list_loaded_capabilities`, `inspect_capability`, `activate_lens`,
  `deactivate_lens`, `list_lenses`, `thane_now`, `thane_assign`,
  `request_core_attention`, `logs_query`. These are bootstrap and
  escape primitives — every loop, however tightly scoped, has them.
  Their Godoc explains why each one earns the slot.

A loop with `tags: []` is deployment-dependent, and the trap is that
the empty case isn't a single behavior:

- **Operator has always-active tags** → tag filtering kicks in. The
  loop sees the always-active tools plus the 12 always-available
  primitives. Scoped, predictable.
- **No always-active tags at this deployment** → `tags: []` falls
  through to "no filtering at all," and the loop sees the *entire*
  tool registry (every native + MCP tool, ~150+ tools at a typical
  site). This is almost never what a focused service loop wants;
  it's a context-blow-up disguised as a config minimum.

Treat `tags: []` as "the default makes a judgment call I haven't
verified." Pass an explicit narrow set instead, even if it's just
`["awareness"]` for an ambient watcher.

## Picking the right tag set

Match the tags to the smallest tool surface that lets the loop do its
job:

- **Curate loop watching HA entities** — needs `home` or `ha` so the
  state/control tools resolve. `awareness` if the loop should see
  ambient context. `documents` if its output goes into a managed doc.
- **Research delegate** — `documents` and `web` cover most one-shot
  investigations. Add `forge` only if the question is repo-shaped.
- **Forge curator** (release digest, PR queue dashboard) — `forge`
  plus `documents` for the output doc.
- **Email triage loop** — `email`. Add `contacts` if the loop should
  ground claims in person records; `notifications` if it should be
  able to escalate.

Whether omitting `tags:` differs from passing `tags: []` depends on
which loop family is doing the launching:

- **`thane_now` / `thane_assign`** — `inherit_caller_tags` defaults to
  true, so omitting `tags:` inherits the caller's capability scope
  (the operator's always-active set plus whatever the caller had
  activated). Pass `inherit_caller_tags: false` along with an
  explicit `tags` array when you need a clean scope that doesn't
  carry over caller context.
- **`thane_curate` / `spawn_loop` / `loop_definition_launch`** — no
  caller-tag inheritance. Omitting `tags:` is the same as passing
  `tags: []`, with the deployment-dependent behavior above. Name
  the tags explicitly for service loops so the surface is stable
  across the loop's lifetime.

## Anti-patterns

- **Tag-as-label.** Tags are not free-form metadata. Each tag binds
  to a real tool surface. Adding `tags: ["server-room"]` doesn't
  pull in any scoped tools — `server-room` isn't a registered tag,
  so the filter resolves to just the 12 always-available primitives
  (no `ha`, no `awareness`, no actual server-watching capability).
  The loop still runs, but it runs blind. Use a real tag name; the
  catalog is the source of truth.

- **Over-tagging "just in case."** Every tag you add pulls in its
  full tool surface, its trailhead/doctrine talents, and any KB
  articles tagged with it. A research delegate with
  `["documents", "web", "ha", "forge", "email", "memory"]` is
  burning context on every iteration for tools 95% of cycles won't
  use. Start with the minimum; expand only when the loop demonstrably
  needs more.

- **Empty tags expecting "baseline."** `tags: []` is not a portable
  "just the basics" knob — see the deployment-dependent split above.
  At a site with operator always-active tags it narrows; at a site
  without, it explodes. Pass an explicit narrow set instead.

- **Adding `ha_admin` without `ha`.** `ha_admin` is an additive
  routing hint, not a self-contained surface — pair it with `ha` or
  the loop ends up with no HA tools at all. The catalog tag
  description spells this out.

## Discovering what a tag opens

Before adding a tag you haven't used before, inspect it:

- `inspect_capability(tag: "<tag>")` — returns the per-tool breakdown
  with source attribution (native, mcp, overlay) and whether the
  operator overlay has excluded any tools. Use this to answer "what
  does adding `forge` actually surface?" without guessing.

- `list_loaded_capabilities` — shows what's already loaded in your
  current scope, including which entries are always-active. Useful
  for telling "this is already covered" from "I need to add this
  tag."

When in doubt, the empirical answer beats the remembered one — a
running loop that worked before is better evidence than your model of
what each tag includes.
