---
tags: [loops]
---

# Loop tagging

When you launch a loop, the `tags` array decides what its
iterations can reach — both the tools they can call and the tagged
knowledge they carry. Get the tag set right and the loop has exactly
what it needs; get it wrong and you've either starved the loop (no
relevant tools) or distracted it with menus it doesn't need. Tagging
is the single most consequential field on a loop launch after the
sleep envelope.

## Tags carry two things: a tool surface and a knowledge bundle

A tag is a binding, and it binds two independent things into any loop
that activates it:

- **A tool surface** — the catalog tools under that tag, which the
  rest of this talent is about.
- **A knowledge bundle** — every KB article whose frontmatter `tags:`
  includes that tag, injected into the loop's context each wake
  (subject to a per-bucket size cap — an oversized article is
  truncated with a marker rather than dropped silently). This is how a
  loop *learns* something durable — a plant primer, a doctrine, a
  house map — instead of re-deriving it each cycle or being handed it
  inline.

A tag can carry tools and no docs (`web`), docs and no tools, or both.
A purpose-built tag like `hor-house-hvac` can be pure knowledge: no
tools of its own, but tagging a doc with it and activating it on a
loop injects that one doc into that one loop. A knowledge-only tag
needs no catalog entry — the match is purely frontmatter `tags:`
against the loop's active tags. (Only the tool-surface half needs
registration; a tag that just routes knowledge does not — see the
tag-as-label anti-pattern below.)

To route a specific document into specific loops:

1. Put a dedicated tag on the doc's frontmatter — `tags:
   [hor-house-hvac]`. Use `tags_all:` instead when the loop must
   carry *all* listed tags before the doc loads.
2. Put that same tag in the `tags` of exactly the loop definitions
   that should read it.

The doc then injects into those loops and nowhere else, and re-scans
every turn — edit the frontmatter or the body and the change
propagates without a restart.

## What's always there

Two things load regardless of your `tags` array:

- **Core tools**: `tag_activate`, `tag_deactivate`,
  `tag_reset`, `tag_inspect`, `lens_activate`,
  `lens_deactivate`, `lens_list`, `thane_now`, `thane_assign`,
  `request_core_attention`, `logs_query`. Every loop has these
  regardless of scope.

- **Core tags.** Some tags load automatically and stay
  loaded — you can't deactivate them. Your system prompt's `## Active
  Tags` section lists what's loaded with `core: true` flagged per
  entry; trust that as ground truth for "what's already in scope."

A loop with `tags: []` is unreliable — depending on what's already
loaded, you may end up with a narrow surface or with the entire tool
registry. Pass an explicit narrow set instead, even just
`["awareness"]` for an ambient watcher.

## Picking the right tag set

Match the tags to the smallest tool surface that lets the loop do its
job:

- **Service loop watching HA entities** (`thane_loop_create` with
  `operation="service"`) — needs `home` or `ha` so the
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
  tags. Pass `inherit_caller_tags: false` along with an explicit
  `tags` array when you need a clean scope.
- **`thane_loop_create` / `spawn_loop` / `loop_definition_launch`** — no
  caller-tag inheritance. Omitting `tags:` behaves the same as `tags:
  []` (unreliable, see above). Name the tags explicitly for service
  loops so the surface is stable across the loop's lifetime.

## Anti-patterns

- **Tag-as-label.** Tags are not free-form metadata. Each tag binds
  to a real tool surface. Adding `tags: ["server-room"]` doesn't
  pull in any scoped tools — `server-room` isn't a registered tag,
  so the filter resolves to just the core tools (no `ha`,
  no `awareness`, no actual server-watching capability). The loop
  still runs, but it runs blind. Use a real tag name; the catalog is
  the source of truth.

- **Routing a doc through a broad capability tag.** Tagging a
  document with `ha` to "get it into the climate loops" injects it
  into *every* loop that activates `ha` — and into your own core loop
  the moment you open the home trailhead. Broad tags leak; a
  purpose-built tag aims. When a doc is for a specific set of loops,
  give it its own tag and activate that tag only where the knowledge
  belongs.

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
Active Tags` section is rendered into every prompt with each loaded
tag's `description`, `tool_count`, `core`, `protected`, and `ad_hoc`
flags. No tool call needed — the answer is already in your context.
Reaching for a tool to retrieve information you already have just
burns a turn.

**What would adding a new tag pull in?** Call `tag_inspect(tag:
"<tag>")`. Returns the per-tool breakdown with source attribution
(native, mcp, overlay). Use this before adding an unfamiliar tag —
the catalog is the source of truth on what's actually behind that
name.

When in doubt, the empirical answer beats the remembered one — a
running loop that worked before is better evidence than your model of
what each tag includes.
