---
kind: doctrine
tags: [loops]
---

# Loops Doctrine

The trailhead above picks the door. This is the craft once you are
through it.

A service loop's `sleep_min` and `sleep_max` set the envelope it
self-paces inside. Its outputs declare what it owns — optional, since a
service loop can act without maintaining a document — and when an output
is declared the running loop writes through a generated tool named for
it: `replace_output_*` for a whole-document rewrite, or
`publish_output_*` when the output declares facets.
If a maintained output is marked `truncated` in Declared Durable
Outputs, read the full document with `doc_read` before replacing it —
the output tool overwrites the entire body.

A document-owning loop carries the read-side document tools —
`doc_read`, `doc_outline`, `doc_section`, `doc_history`, `doc_diff`,
`doc_at` — regardless of its tags: reading what you own, and the
revision history behind it, is never gated. `doc_read` on one of your
own outputs returns the whole document even when it is large — one
read, the full body you are about to replace; `doc_outline` +
`doc_section` page through other large documents. Yesterday's belief,
when a value changed, what a publish actually altered — that record
already exists; walk it instead of reconstructing it. Writes still go
only through the generated output tool.

`thane_loop_create` declares facets directly and derives the working
notes itself; reach for `loop_definition_set` +
`loop_definition_launch` only when a loop needs what the front door
does not expose — several published documents, a facet's format, or
notes placed somewhere specific.

## Who reads this output?

A loop that curates an understanding others consult — the state of a
domain, a watchlist, a standing summary — should declare `facets` on its
maintained document. The loop then curates condensed views of its own
work alongside the full body, and each consuming surface takes the one
it can afford: an ambient overview row takes `status_line`, a search
snippet takes `teaser`, a digest row takes `digest`, and a reader who
wants everything opens the document. Without facets, every consumer pays
for the whole document or gets a blind truncation of it, so a curator
whose value is being *consulted often* is the case for declaring them.

Declaring facets changes the write interface: instead of
`replace_output_*` taking a document body, the loop gets
`publish_output_*` taking one argument per projection. Pass them all in
one call — they are written together so no reader ever sees a status
line describing a state the details have moved past. Do not write the
section headings; they are rendered for you. Each projection has a size
budget, and an over-budget value is rejected rather than trimmed,
because a clipped teaser reads as a fragment with no sign that anything
is missing. Write to the budget rather than near it. The document body
itself has a 96 KiB ceiling on every owner write — the guarantee that
what you write, you can always read back whole in one call. A rejection
at the ceiling is not a retry prompt: the document has outgrown
single-document maintenance, so move detail into linked documents
rather than shaving bytes.

A faceted document created through `thane_loop_create` arrives
scaffolded: its section skeleton is pre-rendered with a placeholder
under each heading, so the body a loop sees on its first wake is
already the shape a correct publish produces. Fill the placeholders;
do not invent structure around them.

Better than a placeholder: seed the first publish. At create time you
have just surveyed the domain — read the neighbors, checked the live
entities, heard what the loop should be about — and the loop
inheriting the document may run on a smaller model. `output.initial`
takes the same arguments as the publish tool and becomes the
document's first published state; the loop revises it from live state
at its first wake. Seed `notes` in the same object with your starting
theory — which signals look sharp, what you expect, what would change
your mind — so the loop's private thinking opens with a belief to
revise instead of a blank page. Author the seed from what you actually
observed; when you have observed nothing, leave the placeholders.

Declare `status_line` for any faceted output; add `teaser` when the
output is something others search or link to, and `digest` when a
reader should be able to act without opening the document.

What makes this concrete on the reading side: `doc_read` takes a `level`,
so any consumer — another loop, a later turn of this one, you — can pull
`status_line` for a glance instead of the whole document. That is the
payoff for curating them. A projection nobody can ask for separately is
just a longer document; a projection that answers a question at one line
is why the loop wrote it.

## Where the thinking goes

What a loop publishes is current state. What it *thinks* — working
theories, what an experiment is showing so far, what it expects next,
what it is unsure of and what would settle it — belongs in its
`working_notes` output: a private document, invisible to every consumer
surface, out of search results and out of other loops' context.

Notes are rewritten, not accumulated. They hold what you believe now,
not a record of what you used to believe, so carry forward what still
holds and drop what you no longer think. A log of superseded theories is
not a place to keep a theory: the next turn would have to reconstruct
your present view by reading its own history, which is the difficulty
these exist to remove.

The smooth path is the `notes` argument on `publish_output_*`: publish
the projections and restate your current thinking in the same call, so
the two never drift apart. The notes output's own `replace_output_*`
writes them when your thinking changes without a publish.

Choose stream wiring by attention cost:

- Use entity subscriptions for ambient state the loop should see on its
  normal turns. `thane_loop_create.entities` creates the initial watch
  set; `add_entity_subscription` / `remove_entity_subscription` with
  `owner` set to the loop's name adjust it later. Ownership is the
  parameter: omit `owner` and the same tools mutate your own
  always-visible subscription set instead — name the loop when the
  loop should carry the watch.
  For a loop watching a room or domain, build the watch set in two
  layers: one `area:<area_id>` entry for ambient coverage — expansion
  honors HA visibility (hidden and diagnostic members stay out), so
  subscribing the whole area is safe by default and tracks the room as
  devices move — plus the few sharp entities carrying `wake`,
  `transitions`, or `history`. Hand-enumerating a room invites
  omissions an area target makes impossible.
  Add `include` metadata flags (`area`, `device`, `labels`,
  `description`, `visibility`, or `all`) when the loop needs
  physical-world context beside the live state, including HA's
  floor/building hierarchy and hidden/enabled salience. Use
  `visibility.context_role` as the quick default-vs-forensic hint.
- When the loop should *act* the moment an entity changes — not wait
  for its next timer — the subscription itself is the trigger: add
  `wake: true` to the entity entry (at creation or later). The loop
  wakes with the change as a `{entity, from, to, ago}` event,
  debounced and coalesced so a chattering sensor can't wakestorm it.
  This is the first door for simple entity-change triggers; reach for
  the HA-automation→MQTT pipeline (`mqtt_wake_add`) only when the
  trigger logic is HA-side — compound conditions, zone dwell,
  templates. Both deliver through the same queue.
- Use event-source `wake_loop` targets when each event from a
  *producer stream* deserves an immediate iteration. Producer tools
  such as `forge_repo_follow` and `media_follow` own those
  subscriptions.

Treat running loops as bi-directional. A service loop can pull you in
via `request_core_attention` when something deserves a decision; you
can push new focus down by adding entities to a running loop's watch
set with `add_entity_subscription` (`owner: <loop name>`), or by
pointing a producer's `wake_loop` target at the loop. Inspect what is already running with
`loop_status` and `loop_definition_get` before launching a parallel
loop — a thriving loop is its own data-dense documentation and is
usually the right thing to extend.

**`request_core_attention` forces a supervisor turn** on your next
iteration — costlier than a normal wake. Reserve it for concerns
that genuinely warrant the extra capacity, not as a routine
notification channel.

Natural-language timing inside a task does not schedule a service loop.
Pick a sleep envelope (sleep_min, sleep_max) tight enough to catch what
matters and loose enough to cost nothing when quiet; the running loop
uses `set_next_sleep` to self-pace inside it. Lint hand-authored
durable definitions before saving them, especially when the envelope,
jitter, or direct domain-tool access matters. Tagged service loops
often want `profile.delegation_gating: "disabled"`.

## Composing the loop

A `thane_loop_create` service spec (`operation="service"`) separates
`intent` (what this loop tracks and why), `instructions` (steering text
prepended to every iteration —
the spec's `Profile.Instructions`), and `output` (the document target
and mode). Get the boundaries right and the loop runs honestly;
muddle them and the loop drifts.

- **Document destinations belong in `output`, not in `intent`.**
  "Update kb:foo with the current state" is the wrong shape for the
  intent; the loop already knows where to write because `output:
  {document: "kb:foo", mode: "maintain"}` told it. Intent names
  *what to observe and why it matters*, not where to write. Document
  management is a framework concern; the intent shouldn't restate it.

- **`instructions` tone shapes the loop's posture.** Earnest,
  mission-focused, concrete about what to look for. Skip distracting
  meta-commentary ("this is our first attempt", "let's see if this
  works") — it leaks into the loop's voice and erodes the focused
  attention the loop is meant to embody. Treat instructions as
  setting an intention, not as commentary about the experiment.

- **Reach for a loop when the source is messy.** Scripts win on
  structured feeds — the parser is short, the failure modes are
  finite. Loops win on human-edited sources with inconsistent
  formatting, drifting layouts, or judgment calls about what changed.
  If you find yourself writing brittle parsing logic to coax a
  script through unstructured input, that's the signal — a loop
  with interpretive instructions handles it more durably.

## Changing a loop that's already running

A running loop carries the config it launched with. Whether an edit
reaches the live instance or waits for a relaunch depends on the tool
you use, not just the field:

- **Retune it live** — task, model, instructions, sleep envelope,
  quality floor, supervisor, max_iter all promote into the running
  loop via `loop_definition_update` at its next turn boundary. No
  relaunch, no lost iteration.
- **Change its watch set live** — entity subscriptions have their own
  door: `add_entity_subscription` / `remove_entity_subscription` with
  `owner` set to the loop's name edit one watch entry in place, also
  without a relaunch.
- **Relaunch-tier changes** — tags and parent bind at launch, so they
  take effect only on a full relaunch: `stop_loop` then
  `loop_definition_launch`, or `loop_reparent` for a parent change (it
  does the stop-and-relaunch for you). A wholesale spec rewrite via
  `loop_definition_set`, or a process restart, relaunches from the
  stored definition too.

Two consequences worth holding:

- **Inheritance resolves at launch, not continuously.** Re-tag a
  container that's already running and neither it nor its live
  children carry the new tag until they relaunch — the container to
  pick up its own new tag, then the children to inherit it. Plan a
  container's shared tags up front when you can.
- **A container with live children can't be stopped in place.** Its
  descendants resolve their home from its current launch, so stopping
  it would strand them. Move or stop the children first
  (`loop_reparent` each, or `stop_loop` them), then relaunch the
  container. That's why re-tagging a busy container is churn.

## Removing a loop

Match the verb to how dead you want it:

- **Gone** — `loop_definition_delete` is the one removal verb: it
  deletes the stored definition AND stops the running instance, and
  its result reports the live outcome — the stopped instance, or that
  none was running. There is no second step for the loop itself.
- **Kept but stopped** — `loop_definition_set_policy state=paused`.
  The definition survives, the instance stops, and it stays stopped
  across restarts until you set it active again.
- **`stop_loop` alone is instance-tier and temporary** for any loop
  whose durable definition is active: the reconciler relaunches it at
  the next boot, commit, or schedule transition. That convergence is
  the machinery working — a durable definition means "this loop should
  be running" — so express durable intent through the definition
  (pause or delete), not the instance.

The loop's documents outlive it. Deleting a loop leaves its declared
outputs in place — the delete result lists them — because the
understanding a curator built may be worth keeping after the curation
stops. Whether they stay is a separate, deliberate decision:
`doc_delete` them if they should not survive the loop. Deleting a
document while its loop still runs does nothing durable — the next
wake rewrites it; stop the loop first.

Report a kill only after verifying it: the delete result's
stopped-instance report is that verification, and `loop_status` on the
name confirms independently. A removal narrated but never verified is
how a loop haunts its owner.

When you need concrete JSON launch patterns, activate `loops_examples`
and adapt the closest recipe minimally.
