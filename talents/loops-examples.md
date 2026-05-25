---
kind: examples
tags: [loops_examples]
---

# Loops Examples

Loops are how Thane runs concurrent attention. The default path is a
**curate loop** — a small subconscious watcher that the core agent
launches with a watch set and a document to keep current, then leaves
alone until either side has something to say.

This file walks the full circle of life so the shape is concrete:
inspect what's already running, launch the watcher, let it self-pace,
let it pull the core's attention when something matters, push new
focus down when the core needs it. Then a few specialty shapes for
work that doesn't fit the curate pattern.

## Look at what's already running first

Each running loop is its own documentation. Before launching anything
new, check what exists — both for "is there already a loop watching
this?" and for "what does a healthy spec for this kind of work look
like?"

- `loop_status` — live registry, with filters for query text, state,
  operation, and a result limit. Returns iteration counts, last wake,
  cumulative and last-turn token use, active tags, metadata, and
  consecutive error counts for every running loop.
- `loop_definition_list` — every durable definition, config and
  overlay together.
- `loop_definition_get(name)` — the full spec for one: task, tags,
  outputs, sleep envelope, profile, supervisor settings, conditions,
  metadata.

Treat the registry as a library of worked examples. A loop that has
run hundreds of healthy iterations is a better template than anything
in this file. If a peer loop already owns the topic, prefer extending
it (via `loop_update_entity_subscriptions`) over launching a parallel
watcher.

## The default path: curate, self-pace, two-way signal

The bi-directional curate loop is the well-worn path. Two-way comms
between a subconscious research task and the core's situational
awareness:

1. **Core launches the watcher** with `thane_curate`. Hand it an
   intent, a watch set, a document to own, and an adaptive sleep
   envelope (see the cadence section below).
2. **Watcher runs at its own pace.** It writes findings to the
   document each cycle and tunes its own next wake with
   `set_next_sleep` (clamped to the envelope it was given).
3. **Watcher pulls the core in when it matters.** When the watcher
   sees something the core needs to decide on, it calls
   `request_core_attention` with a concern and a priority.
4. **Core pushes new focus down when it matters.** When the core
   wants the watcher to track something new, it calls
   `loop_update_entity_subscriptions` to add or remove entities from
   the watcher's set.

The watcher never speaks to the operator directly. It speaks to the
document it owns, and to the core. The core decides what reaches a
human.

### Step 1: Launch the watcher

```json
{
  "name": "server-closet-guardian",
  "intent": "Watch the server-closet environment and equipment health. Document trends. Surface UPS dropouts, dehumidifier failure, or temperature excursions that need attention.",
  "cadence": "30m",
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

`cadence` sets the default; the runtime derives a sleep envelope
around it with light jitter. The watcher writes through the generated
`replace_output_*` tool (for `mode: "maintain"`) or `append_output_*`
(for `mode: "journal"`).

### Step 2: Let the watcher self-pace

Inside its own iteration, the watcher has three steering tools:

- `set_next_sleep` — tune the next wake within the envelope. Cannot
  exceed the persisted `sleep_min` / `sleep_max`.
- `watch_entity` / `unwatch_entity` — adjust its own watch set
  without needing its name.

Pick the envelope at creation time and trust the loop to find its own
rhythm inside.

### Step 3: Pull the core in

When the watcher sees something the core should consider:

```json
{
  "concern": "UPS hor-rack reports 4 minutes battery runtime and 92% load. Brownout protection window is narrowing.",
  "priority": "urgent",
  "context": "Last 30m: load climbed from 78% to 92% after closet AC dropped. No recent grid events."
}
```

`request_core_attention` does not deliver a message to a human. It
queues a supervisor turn on the core loop. The core decides whether
to notify, defer, or absorb. State the concern as a decision or risk,
not as a delivery command.

### Step 4: Push new focus down

When the core decides the watcher should track something new:

```json
{
  "name": "server-closet-guardian",
  "add": [
    {"entity_id": "sensor.closet_ac_state", "history": [3600]},
    {"entity_id": "binary_sensor.utility_brownout"}
  ]
}
```

`loop_update_entity_subscriptions` modifies the running loop's watch
set in place. The watcher sees the new entities on its next wake. Use
`remove: ["entity_id", ...]` to retire watches the core no longer
cares about.

For event-driven wakes (a new release on a repo, a new feed entry),
producer tools like `forge_repo_follow` and `media_follow` take a
`wake_loop` target so the curate loop wakes on the event rather than
its timer.

## Adaptive cadence: pick the envelope, not the tick

The only real judgment at creation is the sleep envelope. The loop
self-pacing inside that envelope handles the rest.

- `sleep_min`: how often this topic can deserve attention at its
  busiest. Tight enough that an urgent change is not missed.
- `sleep_max`: how rarely this topic can be checked without losing
  signal. Loose enough that a quiet day costs nothing.
- `sleep_default`: a sensible mid-point for the first wake.
- `jitter`: 0.1–0.2 unless several peer loops would otherwise
  synchronize and stampede.

The watcher uses `set_next_sleep` to tighten when something is
interesting and loosen when nothing is happening. A burn-ban monitor
might envelope `[1h, 6h]`; a UPS guardian might envelope `[5m, 30m]`;
a daily digest writer might envelope `[12h, 36h]`. In all three the
loop finds its own rhythm.

Words like "hourly" or "every 30 minutes" inside `task` text do not
schedule the loop. Only the sleep envelope does.

## Specialty shapes

### Lint a durable definition before saving

Use `loop_definition_lint` before `loop_definition_set` when
authoring or replacing a persisted service. The linter surfaces
omitted sleep fields, ineffective delegation gating, and task text
that pretends to schedule itself.

### One-shot research that reports back

When the current turn benefits from a side investigation that returns
naturally when done, use `spawn_loop` with
`operation: background_task` and omit completion (the origin context
infers the callback path).

```json
{
  "launch": {
    "spec": {
      "name": "research-current-issue",
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

### Durable service with lifecycle control and supervisor turns

Use `loop_definition_set` (not `spawn_loop`) when the lifecycle —
keep, pause, resume, relaunch — matters. Pair with
`loop_definition_set_policy(state="active" | "paused" | "inactive")`.
Tagged service loops usually want `profile.delegation_gating:
"disabled"` so they can use their own tools directly.

When the loop should mostly run cheap iterations but occasionally
take a more expensive supervisor pass, layer `supervisor: true` plus
`supervisor_prob`, `supervisor_quality_floor`, and a
`supervisor_context` that prompts the model to step back.
`thane_curate` does not expose supervisor fields directly — use
`loop_definition_set` or `spawn_loop` when supervisor turns are
needed.

```json
{
  "spec": {
    "name": "battery-watch",
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
