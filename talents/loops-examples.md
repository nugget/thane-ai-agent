---
name: loops_examples
tags: [loops_examples]
kind: trailhead
teaser: "Open when about to launch any loop-shaped work. Walks you to the right thane_* call."
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
`update_entity_subscriptions`) over launching a parallel watcher.

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

The shapes are not exclusive — a curate loop can spawn a one-shot
research delegate when it needs a side investigation. Pick the
primary shape first.

---
name: loops_examples_curate
tags: [loops_examples_curate]
kind: trailhead
teaser: "Recurring service loops that maintain a managed document over time."
next_tags: [loops_examples_curate_dashboard, loops_examples_curate_journal, loops_examples_curate_circle]
---

# Curate

`thane_curate` creates a service loop that owns a managed markdown
document. Two questions decide which sub-shape fits:

1. **Does each cycle replace the document or append to it?**
   - Replace (idempotent rewrite) → activate
     `loops_examples_curate_dashboard`
   - Append a dated entry → activate `loops_examples_curate_journal`

2. **Does the loop need to escalate decisions to you, or accept new
   focus when you adjust its scope?**
   - Yes (bi-directional) → activate `loops_examples_curate_circle`
     after picking dashboard or journal

## The sleep envelope is the one judgment call

`thane_curate` requires `sleep_min` and `sleep_max`. The loop
self-paces inside that envelope via `set_next_sleep`, which is clamped
to the bounds. Pick bounds to match the topic's metabolism:

- UPS guardian: `[5m, 30m]`
- Burn-ban monitor: `[1h, 6h]`
- Daily digest writer: `[12h, 36h]`

`sleep_default` defaults to the midpoint; `jitter` defaults to 0.1.
Words like "hourly" inside the task text do not schedule the loop —
only the envelope does.

## Tags scope the loop's tools

The `tags` array activates capability tags for the loop's iterations
(things like `home`, `ha`, `awareness`, `documents`). Omit to inherit
the always-active set. A curate loop watching HA entities needs at
least `home` or `ha` so it has the tools to interpret its watch set.

---
name: loops_examples_curate_dashboard
tags: [loops_examples_curate_dashboard]
kind: trailhead
teaser: "Maintain a single dashboard document idempotently each cycle."
---

# Curate: Dashboard (maintain mode)

Use `mode: maintain` when the document should reflect *current state*,
not history. Each cycle rewrites the body; the generated output tool is
`replace_output_<loop_name>`.

```json
{
  "name": "server_closet_guardian",
  "intent": "Watch the server-closet environment and equipment health. Document trends. Surface UPS dropouts, dehumidifier failure, or temperature excursions that need attention.",
  "sleep_min": "10m",
  "sleep_max": "30m",
  "entities": [
    {"entity_id": "sensor.server_closet_temperature"},
    {"entity_id": "sensor.server_closet_humidity"},
    {"entity_id": "sensor.ups_hor_rack_status"},
    {"entity_id": "sensor.ups_hor_rack_battery_runtime"},
    {"entity_id": "switch.dehumidifier"}
  ],
  "output": {
    "document": "kb:dashboards/server-closet.md",
    "mode": "maintain",
    "title": "Server Closet Guardian"
  },
  "tags": ["home", "ha", "awareness"]
}
```

The loop's prompt sees the current document body each turn (truncated
if oversized — in that case, read it with `doc_read` first), then the
model rewrites the body via `replace_output_server_closet_guardian`.

---
name: loops_examples_curate_journal
tags: [loops_examples_curate_journal]
kind: trailhead
teaser: "Append a dated entry to a journal document each cycle."
---

# Curate: Journal (journal mode)

Use `mode: journal` when each cycle adds a *new entry* and prior
entries are preserved. Research notes, decision logs, daily digests.
The generated output tool is `append_output_<loop_name>` — it writes a
new dated section without touching prior entries.

```json
{
  "name": "burn_ban_monitor",
  "intent": "Check the Comal County fire marshal site for the current burn ban status. Note any changes from the prior entry; otherwise note 'no change.'",
  "sleep_min": "1h",
  "sleep_max": "6h",
  "output": {
    "document": "kb:journals/burn-ban.md",
    "mode": "journal",
    "title": "Burn Ban Status Journal"
  },
  "tags": ["web"]
}
```

Journal mode is right when continuity matters — you want to look back
at the trail. Dashboard mode is right when only "right now" matters
and yesterday's state is just noise.

---
name: loops_examples_curate_circle
tags: [loops_examples_curate_circle]
kind: trailhead
teaser: "Bi-directional curate loop: escalates decisions to you, accepts new focus when you adjust its scope."
---

# Curate: The Circle of Life

A curate loop becomes bi-directional when it (a) pulls your attention
when something deserves a decision and (b) accepts new focus from you
when its scope should shift.

## Four steps

1. **You launch the watcher** with `thane_curate` (dashboard or
   journal shape — see those branches).

2. **The watcher runs at its own pace** inside the envelope, tuning
   via `set_next_sleep` and adjusting its own watch set via
   `add_entity_subscription` / `remove_entity_subscription`. You don't
   interact during this phase.

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
   `update_entity_subscriptions`. Adds or removes entities on the
   running loop's watch set in place.

   ```json
   {
     "name": "server_closet_guardian",
     "add": [
       {"entity_id": "sensor.closet_ac_state", "history": [3600]},
       {"entity_id": "binary_sensor.utility_brownout"}
     ]
   }
   ```

   The watcher sees the new entities on its next wake. Use
   `remove: ["entity_id", ...]` to retire watches you no longer care
   about.

For event-driven wakes (a new release on a repo, a new feed entry),
producer tools like `forge_repo_follow` and `media_follow` take a
`wake_loop` target so the curate loop wakes on the event rather than
its timer.

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
background while this turn moves on. Prefer `thane_curate` when the
work is recurring.

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
inline; use `thane_curate` when the work is recurring.

---
name: loops_examples_advanced
tags: [loops_examples_advanced]
kind: trailhead
teaser: "Custom shapes, lifecycle management, supervisor turns, lint before save."
---

# Advanced shapes

When none of `thane_curate`, `thane_now`, or `thane_assign` fits the
work, the lower-level surface is available.

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
`supervisor_context` that prompts the model to step back. `thane_curate`
doesn't expose supervisor fields directly — use `loop_definition_set`
or `spawn_loop` for supervisor-shaped loops.

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
