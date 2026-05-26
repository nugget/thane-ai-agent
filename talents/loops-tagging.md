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

## What's always there

Two things load regardless of your `tags` array:

- **A small set of always-on tools**: `activate_capability`,
  `deactivate_capability`, `reset_capabilities`, `inspect_capability`,
  `activate_lens`, `deactivate_lens`, `list_lenses`, `thane_now`,
  `thane_assign`, `request_core_attention`, `logs_query`. Every loop
  has these regardless of scope.

- **Capability tags that are immutably present.** Some tags load
  automatically and stay loaded — you can't deactivate them. Your
  system prompt's `## Active Capabilities` section lists what's
  loaded with `always_active: true` flagged per entry; trust that as
  ground truth for "what's already in scope."

A loop with `tags: []` is unreliable — depending on what's already
loaded, you may end up with a narrow surface or with the entire tool
registry. Pass an explicit narrow set instead, even just
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
  ground claims in person records. Escalation is not a tag-add
  decision: every loop already has `request_core_attention` from the
  core tool set, and that is the path. The `notifications` tag holds
  the human-egress tools the core loop calls *after* it decides to
  escalate; granting it to a non-core service or delegate loop
  bypasses that boundary, not strengthens it.

Whether omitting `tags:` differs from passing `tags: []` depends on
which loop family is doing the launching:

- **`thane_now` / `thane_assign`** — `inherit_caller_tags` defaults to
  true, so omitting `tags:` inherits the caller's currently-loaded
  capabilities. Pass `inherit_caller_tags: false` along with an
  explicit `tags` array when you need a clean scope.
- **`thane_curate` / `spawn_loop` / `loop_definition_launch`** — no
  caller-tag inheritance. Omitting `tags:` behaves the same as `tags:
  []` (unreliable, see above). Name the tags explicitly for service
  loops so the surface is stable across the loop's lifetime.

## Anti-patterns

- **Tag-as-label.** Tags are not free-form metadata. Each tag binds
  to a real tool surface. Adding `tags: ["server-room"]` doesn't
  pull in any scoped tools — `server-room` isn't a registered tag,
  so the filter resolves to just the always-on primitives (no `ha`,
  no `awareness`, no actual server-watching capability). The loop
  still runs, but it runs blind. Use a real tag name; the catalog is
  the source of truth.

- **Over-tagging "just in case."** Every tag you add pulls in its
  full tool surface, its trailhead/doctrine talents, and any KB
  articles tagged with it. A research delegate with
  `["documents", "web", "ha", "forge", "email", "memory"]` is
  burning context on every iteration for tools 95% of cycles won't
  use. Start with the minimum; expand only when the loop demonstrably
  needs more.

- **Empty tags expecting "baseline."** `tags: []` is not a portable
  "just the basics" knob — see above. Pass an explicit narrow set
  instead.

## Discovering what's loaded vs. what a tag would open

These are two different questions with two different answers.

**What's loaded right now?** Read your own system prompt. The `##
Active Capabilities` section is rendered into every prompt with each
loaded tag's `description`, `tool_count`, `always_active`,
`protected`, and `ad_hoc` flags. No tool call needed — the answer is
already in your context. Reaching for a tool to retrieve information
you already have just burns a turn.

**What would adding a new tag pull in?** Call `inspect_capability(tag:
"<tag>")`. Returns the per-tool breakdown with source attribution
(native, mcp, overlay). Use this before adding an unfamiliar tag —
the catalog is the source of truth on what's actually behind that
name.

When in doubt, the empirical answer beats the remembered one — a
running loop that worked before is better evidence than your model of
what each tag includes.
