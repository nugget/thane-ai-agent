---
name: loops_examples
tags: [loops_examples]
kind: trailhead
teaser: "Open when about to launch any loop-shaped work. Walks you to the right shape and the right door."
next_tags: [loops_examples_curate, loops_examples_now, loops_examples_assign, loops_examples_advanced]
---

# Loops Examples

Loops are how Thane runs concurrent attention. This is the trailhead
for picking the right shape of loop for the work in front of you.

## First move: look at what's already running

Before launching anything new, check what exists. A loop that has run
hundreds of healthy iterations is a better template than anything
below.

- `loop_status` — live registry with filters for query text, state,
  operation, and a result cap. Returns iteration counts, last wake,
  token use, active tags, metadata.
- `loop_definition_list` — every durable definition (config + overlay).
- `loop_definition_get(name)` — full spec for one: task, tags, outputs,
  sleep envelope, profile, supervisor settings, conditions, metadata.

If a peer loop already owns the topic, prefer extending it (via
`add_entity_subscription` with `owner` set to its name) over
launching a parallel watcher.

## Choose the shape of work

Activate the next tag based on what shape this work has:

- **Recurring service work that owns a document** — activate
  `loops_examples_curate`. The default and most-used path. Maintains a
  managed document across iterations; the model adapts its own sleep
  envelope; the document IS the loop's memory.

- **Sync foreground work that must finish before you reply** —
  activate `loops_examples_now`. Wraps a delegate call (`thane_now`)
  that blocks this turn.

- **Async one-shot work that should report back when done** — activate
  `loops_examples_assign`. Detached delegate (`thane_assign`) that
  picks its own moment to deliver.

- **Custom shapes, lifecycle management, or supervisor turns** —
  activate `loops_examples_advanced`. Ad-hoc loops via `spawn_loop`,
  durable definitions you pause/resume, supervisor randomization,
  linting before save.

The shapes are not exclusive — a service loop can spawn a one-shot
research delegate when it needs a side investigation. Pick the
primary shape first.

---
name: loops_examples_curate
tags: [loops_examples_curate]
kind: trailhead
teaser: "Recurring service loops that maintain a managed document over time."
next_tags: [loops_examples_curate_dashboard, loops_examples_curate_circle]
---

# Curate

A service loop is self-paced and recurring. It may maintain a managed
markdown document, but need not — omit outputs entirely for a service
loop that just does recurring work. A document-owning
loop rewrites its document each cycle to reflect current state, and gets
a private working-notes document alongside it for what it currently
believes. Two questions decide the rest:

1. **Will anyone else consult this?** A loop whose value is being read
   by other turns, other loops, or an ambient surface should declare
   `facets` so each reader takes the length it can afford.
   `thane_loop_create` declares them directly on its `output` argument;
   a loop nobody reads but its owner needs no facets at all.

2. **Does the loop need to escalate decisions to you, or accept new
   focus when you adjust its scope?**
   - Yes (bi-directional) → activate `loops_examples_curate_circle`

## The sleep envelope is the one judgment call

`thane_loop_create` requires `sleep_min` and `sleep_max`. The full spec
does not — it defaults to 30s–5m, which is wrong for almost anything
worth curating, and wrong quietly: the loop runs, just far too often.
Set the envelope deliberately whichever door you use. The loop
self-paces inside it via `set_next_sleep`, which is clamped to the
bounds. Pick bounds to match the topic's metabolism:

- UPS guardian: `[5m, 30m]`
- Burn-ban monitor: `[1h, 6h]`
- Daily digest writer: `[12h, 36h]`

`sleep_default` defaults to the midpoint; `jitter` defaults to 0.1.
Words like "hourly" inside the task text do not schedule the loop —
only the envelope does.

## Tags scope the loop's tools

The `tags` array activates tags for the loop's iterations
(things like `home`, `ha`, `awareness`, `documents`). Omit to inherit
the core tag set. A service loop watching HA entities needs at least
`home` or `ha` so it has the tools to interpret its watch set.

---
name: loops_examples_curate_dashboard
tags: [loops_examples_curate_dashboard]
kind: trailhead
teaser: "Curate a domain others consult: one document, published at several fidelities."
---

# Curate: Dashboard (faceted publish)

A loop that keeps an understanding current for other readers should
publish it at more than one length. Declaring `facets` on a maintained
output curates condensed views alongside the full body, so an ambient
row takes `status_line`, a search snippet takes `teaser`, a digest row
takes `digest`, and a reader who wants everything opens the document.
Without facets every consumer pays for the whole document or gets a
blind truncation of it.

`thane_loop_create` builds this shape directly: declare the facets on
its `output` argument and the working notes are derived for you, with
both documents scaffolded before launch — the faceted one with the
exact section skeleton its publish tool fills. The full spec below is
the same loop authored by hand, for when you need a field the front
door does not expose (several published documents, a facet's format,
notes placed somewhere specific). Lint first on that path —
`loop_definition_lint` takes the same spec and catches mistakes before
anything persists.

```json
{
  "name": "server_closet_guardian",
  "intent": "Keep the current state of the server closet — environment, power, and equipment health — legible to anyone who asks, and surface excursions that need attention.",
  "task": "Read the current sensor values and the document you maintain. If the state has moved materially, publish all projections together. If nothing has changed, publish nothing and record why in working notes.",
  "operation": "service",
  "sleep_min": "10m",
  "sleep_max": "30m",
  "subscriptions": [
    {"entity_id": "sensor.server_closet_temperature"},
    {"entity_id": "sensor.server_closet_humidity"},
    {"entity_id": "sensor.ups_hor_rack_status"},
    {"entity_id": "switch.dehumidifier"}
  ],
  "tags": ["home", "ha", "awareness", "documents"],
  "outputs": [
    {
      "name": "closet_state",
      "type": "maintained_document",
      "ref": "kb:dashboards/server-closet.md",
      "facets": ["status_line", "teaser", "digest"],
      "purpose": "Current state of the server closet for anyone who asks."
    },
    {
      "name": "notes",
      "type": "working_notes",
      "ref": "kb:dashboards/server-closet-notes.md"
    }
  ]
}
```

## Publishing

Declaring facets swaps the generated tool: instead of
`replace_output_closet_state` taking a body, the loop gets
`publish_output_closet_state` taking one argument per projection. Pass
them all in one call — they are written together so no reader sees a
status line describing a state the details have moved past. Do not write
the section headings; they are rendered for you.

```json
{
  "status_line": "Closet 21.4°C, 38% RH, UPS on mains, dehumidifier idle.",
  "teaser": "Server closet is stable. Temperature and humidity have held inside normal range for six hours; the UPS is on mains power with a full charge, and the dehumidifier has not needed to run since morning.",
  "digest": "Environment: 21.4°C (range 20.8–22.1 over 24h), 38% RH, both comfortably inside bounds...",
  "full": "# Server Closet\n\n## Environment\n\n...",
  "notes": "Humidity tracks the dehumidifier duty cycle with about a 40-minute lag, so a rising reading right after it stops is expected rather than a fault. Watching whether the morning peak keeps climbing week over week — if it does, the closet is gaining a moisture source I have not found yet."
}
```

Each projection has a rune budget — 120 for `status_line`, 500 for
`teaser`, 2048 for `digest` — and an over-budget value is rejected
rather than trimmed, because a clipped projection reads as a fragment
with no sign that anything is missing. Write to the budget, not near it.

Think about what each length is *for* rather than truncating the one
above it. The status line is a glance: what is true right now. The
teaser is what a reader needs to decide whether to open the document.
The digest is enough to act on without opening it.

## Where the reasoning goes

The `working_notes` output holds this loop's current thinking — internal
by construction, never projected into search or another loop's context.
Put your present view there: what you believe is happening, what you are
watching, what would change your mind. Rewrite it rather than appending
to it, so the next turn reads a position instead of reconstructing one
from a history of superseded ones. `publish_output_*` takes a `notes`
argument, so a publish and the thinking behind it are one call.

Published projections carry current state, not the story of how it got
there. Keeping those apart is what lets the document stay short.

---
name: loops_examples_curate_circle
tags: [loops_examples_curate_circle]
kind: trailhead
teaser: "Bi-directional service loop: escalates decisions to you, accepts new focus when you adjust its scope."
---

# Curate: The Circle of Life

A service loop becomes bi-directional when it (a) pulls your attention
when something deserves a decision and (b) accepts new focus from you
when its scope should shift.

## Four steps

1. **You launch the watcher** with `thane_loop_create`
   (`operation: service`, dashboard or journal shape — see those
   branches).

2. **The watcher runs at its own pace** inside the envelope, tuning
   via `set_next_sleep` and adjusting its own watch set via
   `watch_entity` / `unwatch_entity` (the in-loop door — no name
   needed, the loop is baked in). You don't interact during this
   phase.

3. **The watcher pulls you in when something matters** via
   `request_core_attention`. This forces a supervisor turn on your
   next iteration — costlier than a normal wake, so the watcher
   should reserve it for concerns that genuinely warrant the extra
   capacity.

   ```json
   {
     "concern": "UPS hor-rack reports 4 minutes battery runtime and 92% load. Brownout protection window is narrowing.",
     "priority": "urgent",
     "context": "Last 30m: load climbed from 78% to 92% after closet AC dropped. No recent grid events."
   }
   ```

   The concern arrives stated as a decision or risk, not as a
   delivery command. You decide whether to notify, defer, or absorb.

4. **You push new focus down when something matters** via
   `add_entity_subscription` / `remove_entity_subscription` with
   `owner` naming the loop. Each call adjusts one entity on the
   running loop's watch set in place.

   ```json
   {
     "entity_id": "sensor.closet_ac_state",
     "owner": "server_closet_guardian",
     "history": [3600]
   }
   ```

   The watcher sees the new entity on its next wake. Use
   `remove_entity_subscription` with the same `owner` to retire
   watches you no longer care about.

For wakes on a *single entity's changes* — a door opening, a pump
starting — the loop's own subscription is the trigger: `wake: true`
on the entity entry (in `thane_loop_create.entities`, via
`add_entity_subscription` with `owner`, or `watch_entity` from
inside). No automation, no topic, debounced by default. Reach past
this door only when the trigger logic can't be "this entity changed."

For event-driven wakes (a new release on a repo, a new feed entry),
producer tools like `forge_repo_follow` and `media_follow` take a
`wake_loop` target so the service loop wakes on the event rather than
its timer.

For wakes triggered by *HA-side derived conditions* — compound
triggers, zone dwell, templates, anything an HA automation decides —
register the loop with `mqtt_wake_add` and pair it with an HA
automation whose action publishes to the same topic. The two sides
are independent artifacts that share only the topic string; the
topic string IS the contract. (The worked example below is exactly
this shape: the trigger is "presence transition AND morning AND
entering the office," which is HA-automation logic, not a bare
entity change.)

### Worked example: morning-briefing loop on Alice's office arrival

The situation: Alice's HA presence usually transitions `not_home →
office` once each weekday morning, when she walks in. That
arrival is a meaningful semantic moment — she's at her desk, the
day is starting, anything that accumulated overnight (email, PRs,
household notes) is now relevant to surface. The pairing below
fires a Thane briefing loop on that exact transition.

**The HA-side automation** (created via `ha_automation_create`)
triggers on the zone transition specifically — not "person.alice
is at the office," which would re-fire on every brief HA state
flap. Triggering on the transition means once-per-arrival
semantics, which is what the briefing wants:

```json
{
  "config": {
    "alias": "Alice arrived at office — wake Thane briefing",
    "description": "When Alice's presence transitions from anywhere to office, publish to thane/wake/alice_arrived_office so the morning-briefing loop fires once.",
    "trigger": [
      {
        "platform": "state",
        "entity_id": "person.alice",
        "to": "office"
      }
    ],
    "condition": [
      {
        "condition": "time",
        "after": "06:00:00",
        "before": "12:00:00"
      }
    ],
    "action": [
      {
        "service": "mqtt.publish",
        "data": {
          "topic": "thane/deepslate/wake/alice_arrived_office",
          "payload": "{\"source\": \"ha\", \"trigger\": \"alice_arrived_office\", \"timestamp_iso\": \"{{ now().isoformat() }}\"}"
        }
      }
    ],
    "mode": "single"
  },
  "metadata": {
    "area_id": "office",
    "label_ids": ["presence", "thane-bridge"]
  }
}
```

A few choices that matter:

- **`platform: state` with `to: "office"`** fires on the *edge*
  (entering office), not the level (currently in office). HA
  state-trigger semantics give once-per-entry for free; no
  manual debouncing.
- **Time condition** keeps the briefing morning-shaped. If Alice
  steps out for lunch and comes back at 13:00, that's a different
  context and not what this loop is for.
- **`mode: single`** means a second trigger while the automation
  is still running won't double-fire. With `mqtt.publish` this
  matters less, but it's the safe default for any HA-side action.
- **Payload as JSON** carries useful context to the loop side —
  and one field IS parsed for routing: `target_loop` (a loop
  definition name) re-addresses the wake to that loop, so one
  shared wake topic can serve many automation-authored targets
  without per-topic registrations. An unresolvable `target_loop`
  falls back to the subscription's `wake_loop`. The timestamp
  helps the loop notice when it's reacting to a stale message
  (network flap, broker replay).
- **Topic convention `thane/{device_name}/wake/{purpose}`** is
  the documented shape for instance-directed wakes — multiple
  Thane installations can share a broker without crosstalk,
  because each installation listens only under its own
  device-name segment.

**The Thane-side registration** ties a loop to that topic. The
`wake_loop` field carries a `LoopWakeTarget` — name the loop by
its `name` (or `loop_id` if you have it from `loop_status`):

```json
{
  "topic": "thane/deepslate/wake/alice_arrived_office",
  "wake_loop": {
    "name": "alice_morning_briefing"
  }
}
```

`wake_loop` also accepts `force_supervisor` (run the loop's next
iteration as a supervisor turn — costlier; reserve for genuinely
high-stakes wakes), `priority` (`low` / `normal` / `urgent`), and
`instructions` (extra prose the wake-handler can inject into the
loop's pending-notification context). Most cases just need
`name`. Verify the target exists via `loop_status` before
persisting — `mqtt_wake_add` resolves the target at
message-arrival time, not at registration, so a typo here fails
silently the first time the event fires.

The named loop's `Task` is where the briefing's intent lives:

```text
Alice just arrived at the office. Surface the small set of things
that benefit her in the next 30 minutes:

- Anything from overnight email that needs a response by today.
- PRs she's been requested to review where CI is now green.
- Any unresolved household concerns the family raised after she
  left yesterday.
- Calendar conflicts or schedule shifts for the next 4 hours.

Compose a brief Signal message to her summarizing what you found —
two to four bullets, no preamble. If nothing material surfaced,
send nothing. Use her contact preferences (signal preferred,
short-form, plain text).
```

A few framing choices worth naming:

- **Time-boxed scope** ("next 30 minutes," "next 4 hours") keeps
  the loop from sprawling. Without those, "surface relevant
  things" is unbounded; with them, the loop knows what to filter.
- **"Send nothing if nothing material"** is the off-switch.
  Without it, the loop will manufacture a briefing every morning
  even when the day is quiet, and the model trains itself (and
  Alice) to ignore Thane's voice. Silence is a feature.
- **Contact-preference grounding** routes through the
  `contacts` directory — the loop doesn't decide the channel,
  it reads it from Alice's record. Same payoff as the email
  trust gate: keep delivery routing in the system, not in
  per-turn judgment.

**Operations on the wake subscription:**

```json
{}
```

`mqtt_wake_list` returns currently registered subscriptions —
each entry includes a `subscription_id`, the topic filter, the
source (`config` for site-pinned subs, `runtime` for ones added
via `mqtt_wake_add`), and the `wake_loop` target. Useful before
adding a new one to confirm you're not accidentally creating a
duplicate, and **required first** when retiring one, since
removal is by ID:

```json
{
  "subscription_id": "01964fa3-7c2e-7d12-9a4b-1b2c3d4e5f6a"
}
```

`mqtt_wake_remove` retires a runtime binding by its subscription
ID (read from `mqtt_wake_list`). **Config-defined subscriptions
cannot be removed via this tool** — those live in the operator's
config file and have to be retired there. The HA automation can
stay registered if other consumers need the topic; conversely,
removing the HA automation without removing the wake subscription
leaves the loop quietly waiting for a message that never comes.
Both sides are independent — manage them as a pair when you can.

**Why this shape is the canonical event-bridge:**

- HA owns the *observation* (Alice's location, sensor states,
  schedule). Its trigger semantics are mature and well-tested.
- Thane owns the *response* (composing the briefing, deciding
  what's material, picking the channel). The loop's model-driven
  reasoning is what HA's automation YAML can't express.
- MQTT is the dumb pipe between them. The topic name carries the
  semantics (`thane/wake/alice_arrived_office`); the payload is
  optional context.

The same pattern fits any "HA notices something → Thane decides
what to do about it" workflow: sump pump cycled twice in an hour,
garage door open past 10pm, child's bedtime motion detector
quiet for 30 minutes after lights-off, freezer temperature
drifting. Each is a different topic, a different loop, but the
same two-artifact pairing.

---
name: loops_examples_now
tags: [loops_examples_now]
kind: trailhead
teaser: "Sync foreground delegate — must finish before this turn replies."
---

# Now (sync delegate)

`thane_now` runs a bounded delegate that blocks the current turn.
Returns its result inline. Use this when the work must complete before
you reply and you don't want to fragment the conversation.

```json
{
  "task": "Look up the current open PRs on nugget/thane-ai-agent assigned to the user, and return their titles and ages.",
  "profile": "research"
}
```

The delegate runs in a child loop with its own tool surface (chosen by
profile), executes the task, and returns its content as the tool
result. Cost is sync model spend on the delegate's iterations.

Prefer `thane_assign` (the next tag) when the work can run in the
background while this turn moves on. Prefer `thane_loop_create` with
`operation: service` when the work is recurring.

---
name: loops_examples_assign
tags: [loops_examples_assign]
kind: trailhead
teaser: "Async one-shot delegate — reports back when done; this turn continues."
---

# Assign (async one-shot delegate)

`thane_assign` launches a detached delegate that runs in the
background. Result is delivered later via the current conversation or
channel context. The launching turn continues immediately.

```json
{
  "task": "Investigate why the email-poller loop reported zero new messages in the last 6 hours. Check the email handler logs, the IMAP connection state, and any recent config changes. Report findings.",
  "profile": "research"
}
```

The completion delivery path is inferred from the launch context
(current Signal conversation, OWU session, etc.). The model that
receives the completion sees the original task and the delegate's
final reply.

Use this for side investigations the operator should hear about but
that don't need to block. Use `thane_now` when the answer is needed
inline; use `thane_loop_create` with `operation: service` when the work
is recurring.

---
name: loops_examples_advanced
tags: [loops_examples_advanced]
kind: trailhead
teaser: "Custom shapes, lifecycle management, supervisor turns, lint before save."
---

# Advanced shapes

When none of `thane_loop_create`, `thane_now`, or `thane_assign` fits
the work, the lower-level surface is available.

## Lint before saving a durable definition

`loop_definition_lint` surfaces omitted sleep fields, ineffective
delegation gating, task text that pretends to schedule itself, and
other authoring mistakes. Run it before `loop_definition_set` when
authoring or replacing a persisted service by hand.

## Lifecycle: pause, resume, delete

For durable definitions managed by `loop_definition_set`:

- `loop_definition_set_policy(name, state: "paused")` — stops without
  forgetting; resume by setting state back to `"active"`.
- `loop_definition_set_policy(name, state: "inactive")` — disables.
- `loop_definition_delete(name)` — removes from the overlay (config
  definitions are immutable).

Stored definitions usually want `profile.delegation_gating: "disabled"`
so tagged service loops can use their own tools directly without
routing through the orchestrator-delegate gating pattern.

## Supervisor turns on service loops

When a service loop should mostly run cheap iterations but occasionally
take a more expensive supervisor pass, set `supervisor: true` plus
`supervisor_prob`, `supervisor_quality_floor`, and a
`supervisor_context` that prompts the model to step back.
`thane_loop_create` doesn't expose supervisor fields directly — use
`loop_definition_set` or `spawn_loop` for supervisor-shaped loops.

```json
{
  "spec": {
    "name": "battery_watch",
    "enabled": true,
    "task": "Maintain a current view of battery health across the property. Notice trends, anomalies, and devices that deserve attention. Keep the state document concise and trustworthy.",
    "operation": "service",
    "completion": "none",
    "sleep_min": "10m",
    "sleep_max": "30m",
    "sleep_default": "15m",
    "jitter": 0.2,
    "supervisor": true,
    "supervisor_prob": 0.15,
    "quality_floor": 4,
    "supervisor_quality_floor": 9,
    "supervisor_context": "Supervisor turn. Step back from individual readings, look for cross-device patterns or weak assumptions, and decide whether anything now deserves escalation or a sharper hypothesis.",
    "profile": {
      "mission": "background",
      "delegation_gating": "disabled",
      "initial_tags": ["home", "knowledge", "documents"],
      "instructions": "Maintain one durable state document. Use the journal when something materially changes. Call set_next_sleep when the next wake should be meaningfully shorter or longer than the default cycle."
    }
  }
}
```

Put the main prompt in `spec.task`, not top-level `launch.task`, so
`supervisor_context` applies cleanly on supervisor turns.

## Ad-hoc and one-shot research

When the work is loop-shaped but shouldn't become a durable
definition, use `spawn_loop` with `operation: background_task` and
omit completion (the origin context infers the callback):

```json
{
  "launch": {
    "spec": {
      "name": "research_current_issue",
      "task": "Investigate the current issue from multiple angles, keep concise notes in a managed document if needed, and report back with the strongest answer once the uncertainty has collapsed.",
      "operation": "background_task",
      "profile": {
        "mission": "background",
        "initial_tags": ["knowledge", "documents"],
        "instructions": "Prefer the smallest tool surface that can collapse uncertainty. Use document tools for durable notes."
      }
    }
  }
}
```

For most one-shot work, `thane_assign` is cleaner — `spawn_loop` is
for the cases where you need to express the spec directly.
