---
name: ha
tags: [ha]
kind: trailhead
teaser: "Open for the home — the whole house at a glance, a room, a device, live state, control, or automations."
next_tags: [ha_control, ha_automate]
---

# Home Assistant

The HA surface is the largest lever this agent has on the physical
world. The watched-entity snapshot you carry by default is a keyhole —
a handful of subscribed sensors, not the house. This is the door. Behind
it: every room, every device, full history and registry, live and
searchable. When the conversation turns toward home, the real picture
lives here — open this rather than answering the home from the keyhole.

Reading is cheap, safe, and loaded right here — the tools below are
available the moment this tag is active. Control is fast and
stale-ID-prone; automation authoring is durable and breakable. Each of
those is one deliberate step further in (`ha_control`, `ha_automate`),
because acting on the home spends trust, not just tokens.

## Start wide: the whole house at a glance

`ha_home_snapshot` is the curated "how's the house right now" overview —
the native answer to "what's going on at home", "is the house buttoned
up", or "who's home". When home curiosity is broad rather than aimed at
one thing, start here:

```json
{
  "include_energy": true
}
```

Leads with what's actionable: anomalies (offline / in alarm), then
security/openings (open doors and windows, unlocked locks, armed or
triggered alarm panels), then presence (who's home vs away), then
climate. A top-level `summary` gives the counts at a glance, and
`status: "quiet"` means nothing is offline, open, unlocked, or armed.
Pass `include_energy` for a power/energy section and `include` for
per-entity metadata.

## Narrow to a room

`get_area_activity` is the whole-area perception view — the native
answer to "what's in the office" or "is everything okay in the kitchen":

```json
{
  "area": "office",
  "include": {"device": true, "labels": true}
}
```

Returns the area with its floor/building context and its entities
grouped by salience — anomalies (offline / alarm) first, then active
devices, recent changes, ambient sensors, and the stable remainder —
plus a transition timeline and counts of what was filtered out
(disabled/hidden/diagnostic/config). Default-context entities only;
pass `include_hidden` or `include_diagnostic` for a forensic pass.

## Narrow to a device

`ha_device` is the whole-device perception view — the native answer to
"show me the thermostat device", "what does the front-door sensor
expose", or "is this device healthy":

```json
{
  "device": "front door lock",
  "include": {"labels": true}
}
```

Returns the full device-info card — manufacturer, model, firmware,
serial, network connections (MAC and friends), area, labels, integration,
and via_device — plus every child entity it owns, grouped exactly
the way Home Assistant's own device page groups them — `controls` (the
actionable primaries: lights, switches, climate), `sensors` (the
read-only primaries), `configuration` (tuning knobs), and `diagnostic`
(health counters) — with an availability rollup (how many entities are
reporting). This is explicit inspection, so unlike the enumeration tools
it shows **hidden** entities too, each marked `"hidden": true`: naming a
device means you want its whole instrument panel, including the config
and diagnostic entities HA keeps off its generated dashboards. The
`configuration` and `diagnostic` groups are capped for a device with a
long tail of knobs, with an honest `*_truncated_count`. Resolves by
`device_id` or by name — user-assigned or registry name, with a
substring fallback, returning candidates when a name is ambiguous. Reach
for this instead of discovering a device's child entities one
`ha_get_state` at a time.

## Find one entity by description

`ha_find_entity` does fuzzy lookup by description, optionally narrowed
by area or domain:

```json
{
  "description": "ceiling light",
  "area": "office",
  "domain": "light",
  "include": {"all": true}
}
```

Returns the best match with a confidence score, or candidate
entity_ids when the description is ambiguous. The natural precursor
to `ha_get_state` or `ha_control_device`. Area filters use the HA
registry, including device-inherited area, instead of only blending the
area words into the fuzzy query.

## Read one entity you can already name

`ha_get_state` returns the current state and attributes when the
entity_id is already in hand:

```json
{
  "entity_id": "light.office_main"
}
```

The fastest path when the entity_id is already known. Returns a
curated, class-aware projection — semantic state plus the attributes
that matter for that class (a climate entity shows mode, current,
target, and hvac_action; a lock shows battery and jammed-vs-unlocked;
an event shows what fired and when) — not a raw attribute dump.

Add `include` when physical-world metadata would improve reasoning:

```json
{
  "entity_id": "sensor.office_temperature",
  "include": {"area": true, "device": true, "labels": true, "description": true}
}
```

The same `include` shape works on `ha_find_entity`, `ha_list_entities`,
and entity subscriptions. `all: true` enables every supported metadata
projection. Metadata can include resolved area, owning device, HA
labels from the entity/device/area, aliases, category, platform, device
class, floor/building hierarchy, visibility/enabled status, and human
descriptions when HA has them. Hidden-but-enabled entities are still
available for focused research; treat HA visibility as a default-context
salience hint, not as proof the data is unimportant. `visibility.context_role`
summarizes the model-facing role as `default`, `hidden`, `diagnostic`,
`config`, or `disabled`.

## Find everything matching a live-state condition

`ha_search_states` filters by *current state* across all domains —
the native answer to "what's on right now," "what doors are open,"
"which sensors are unavailable," "what batteries are low":

```json
{
  "state": ["on"],
  "area": "office"
}
```

```json
{
  "attribute": "battery",
  "comparison": "<",
  "value": 20
}
```

Filters compose (AND): pair a `state` set with a `domain` or `area`,
or use a numeric `attribute`/`comparison`/`value` predicate for
threshold questions. This is the right reach instead of fanning out
many `ha_get_state` calls or listing a whole domain and eyeballing it.
Add `include` for area/device/label/visibility metadata on each match.

**Hidden entities.** `ha_search_states` and `ha_list_entities` default
to the entities the operator lets Home Assistant show — hidden ones
(the diagnostics and config an operator curated off their dashboards)
are excluded, and their count comes back as `hidden_excluded` so you
know they exist. Respect that default: hidden is operator intent, not
an obstacle. When you specifically need them — a device's power draw,
a diagnostic sensor — pass `include_hidden: true` (each returned hidden
entity is marked `hidden`), or read the one entity directly with
`ha_get_state`, or inspect the whole device with `ha_device`, which
shows everything a device exposes the way HA's device page does.

## Enumerate a domain or name pattern

`ha_list_entities` enumerates by domain:

```json
{
  "domain": "light",
  "include": {"area": true, "labels": true}
}
```

Right for discovery ("what lights exist?") or for iterating over a
set ("check every door sensor"). Returns structured entity rows; add
metadata when the model needs room, device, label, or description
context to choose the right follow-up.

For substring or cross-domain matches, pass a `pattern` glob over the
full entity_id instead of (or alongside) `domain`:

```json
{
  "pattern": "binary_sensor.*door*"
}
```

`*` matches any run of characters, so `*_temperature` spans domains and
`light.office_*` narrows within one. Domain and pattern combine (AND)
when both are given. This is for *naming*-shaped discovery; to find
entities by their live *state*, reach for `ha_search_states` instead.

## See how something has trended

`ha_history` summarizes one entity's recorder history over a lookback
window — the native answer to "how has the office temperature moved over
the last 24h" or "how many times did the front door open today":

```json
{
  "entity_id": "sensor.office_temperature",
  "lookback_seconds": 86400
}
```

Returns a numeric trend (min/max/start/end/delta + rising/falling/flat)
for numeric entities, or a discrete change summary (change count +
recent states) for non-numeric ones. To trend a value that lives in an
attribute rather than the state — a `climate` entity's
`current_temperature`, say — pass `attribute`. Defaults to a 24h window,
clamped to 30 days.

For *sustained*, every-turn attention to an entity, don't poll this from
a loop's turn budget — subscribe via `awareness` with history windows
and let the trend stay current between turns for free.

## Search the registry (areas, labels, devices, entities)

`ha_registry_search` searches areas, labels, devices, and entities
in one call:

```json
{
  "query": "kitchen",
  "limit": 8
}
```

Returns matches across all four registry categories with relevance
scores. The right tool when:

- Authoring an automation (you need real label IDs, area IDs, and
  entity IDs — not guesses).
- Investigating a room ("what's actually in here?").
- Following a label across categories ("everything tagged security").

## When you need to act, step further in

The read tools above answer "what's happening." Two branches go
further, and each is its own activation because the cost of a mistake
rises:

- **Change something** — activate `ha_control`. The find → act → verify
  pattern, with safety on stale entity IDs.
- **Manage Home Assistant's own automations** — activate `ha_automate`.
  List with activity stats, inspect, create, update, delete.

## The constants across every branch

- **`ha_call_service` does not validate entity IDs.** A typo or stale
  ID returns success and silently does nothing. This is the single
  most consequential gotcha in the HA surface; every action-shaped
  branch carries the verify-after pattern.
- **`ha_control_device` is the high-level path; `ha_call_service` is the
  low-level path.** Use `ha_control_device` unless you already have the
  exact entity_id from a recent lookup *in the same turn*.
- **Sustained attention is `awareness`'s job, not `ha`'s.** A one-off
  state check uses the read tools here; a loop watching a room subscribes
  via `awareness` and lets entity state stay current between turns.
- **Delivery and escalation are `notifications`'s job.** When the
  next move is "tell someone about this," activate notifications;
  HA's tools are about state and control, not interruption.

## Presence and zones

Person entities carry `in_zones` — the full list of zones someone is in
right now (zones nest, so it can be several at once). The `state` field
reports only the *smallest* zone: a person in a zone inside the home
shows that zone's name as state, and `zone.home` in their `in_zones` is
what says they're still home. Read membership from `in_zones`, not from
`state == "home"`.

For the reverse question — "who's in zone X" — read the zone entity
itself: its state is the live occupant count and its `persons` attribute
lists them (`ha_get_state` on `zone.home`, or `ha_list_entities` with
`domain: zone` to survey every zone at once). No person-by-person sweep
needed. Person entities whose location comes from presence scanners
carry no coordinates — never assume `latitude`/`longitude` exist.

## Cross-references

- For sustained entity attention across loop iterations, bounce to
  `awareness` and subscribe — don't poll `ha_get_state` from a loop's
  turn budget when a subscription will keep it current for free.
- For "who is home / what zone is X in," `awareness` owns
  presence-shaped questions even though they're technically HA
  entities. Presence has its own context grammar.

---
name: ha_control
tags: [ha_control]
kind: trailhead
teaser: "Change device state — find → act → verify, with safety on stale IDs."
---

# Control

You want to change something. The single most important pattern in
HA control is the three-step move; the second is choosing between
`ha_control_device` (high-level) and `ha_call_service` (low-level).

## The find → act → verify pattern

Never trust an action's success alone. Stale entity IDs return
success and silently do nothing. For anything that matters:

1. **find** the entity — `ha_find_entity` if working from a
   description, `ha_get_state` to confirm a known entity_id is still
   real.
2. **act** — `ha_control_device` (preferred) or `ha_call_service`.
3. **verify** — `ha_get_state` after the action, confirm the new value
   actually took.

The pattern is overkill for cheap idempotent actions ("turn on a
light that's probably already on"). It is **mandatory** for anything
involving locks, garage doors, alarms, safety devices, scenes that
affect multiple rooms, or any configuration change. The cost of a
silent no-op there is real.

## High-level: ha_control_device

`ha_control_device` accepts a description and an action; it does the
lookup internally:

```json
{
  "description": "kitchen ceiling light",
  "action": "turn_on",
  "area": "kitchen"
}
```

Right for voice-shape commands and any case where the entity_id
isn't already in hand. The action vocabulary matches HA's natural
services (turn_on, turn_off, toggle, set_brightness, etc.).

When `ha_control_device` reports ambiguity, that's the find-step doing
its job — re-call with a tighter description or a specific `area` /
`domain` to disambiguate, don't fall through to `ha_call_service` with
a guessed entity_id.

## Low-level: ha_call_service

Unsure of a service name or its fields? `ha_list_services` is the
catalog: bare call for a directory of every domain's service names;
`domain` for full field detail on all of that domain's services;
`"domain.service"` (e.g. `"light.turn_on"`) for just that one service —
descriptions, required flags, examples, and whether it accepts a
target block. Check it before guessing; a wrong service name costs a
failed call.

`ha_call_service` addresses one verified entity_id, or fans out with a
`target` block. Multi-device intent is ONE call, not N: "turn off the
office lights" is a single call targeting the area —

```json
{
  "domain": "light",
  "service": "turn_off",
  "target": { "area_id": "Office" }
}
```

Targets take `entity_id`, `device_id`, `area_id`, `floor_id`, and
`label_id` (string or array each), and areas/floors/labels/devices
accept human names as well as registry IDs — names resolve against the
registry, and unknown references fail fast with the known names instead
of HA's silent no-op. HA skips hidden entities in area/floor/label
targets; that's the operator's curation, not a bug. The response
reports which entities actually changed state — zero changes with a
note usually means everything was already in the requested state.

Single-entity form, with the exact entity_id:

```json
{
  "entity_id": "light.office_main",
  "domain": "light",
  "service": "turn_on",
  "data": {
    "brightness_pct": 60,
    "color_temp_kelvin": 3000
  }
}
```

Use when:

- You already have the exact entity_id from a recent `ha_find_entity`
  or `ha_get_state` (within the same turn — not from memory of a
  previous conversation).
- The service needs structured `data` that `ha_control_device`'s
  vocabulary doesn't cover: specific color temperatures, scene
  activation with arguments, climate setpoints, media player
  payloads, etc.

**Do not** pull an entity_id from memory and reach for
`ha_call_service`. Always re-verify with `ha_find_entity` or `ha_get_state`
first — entity IDs change when devices are renamed or reconfigured,
and a stale ID is the canonical silent-no-op trap.

## Cross-references

- For "encode this rule durably so it fires whenever X happens"
  instead of one-shot control, bounce to `ha_automate`.
- For "I changed the thing and now want to tell someone," bounce to
  `notifications` after the verify step.

---
name: ha_automate
tags: [ha_automate]
kind: trailhead
teaser: "Manage HA's own automations — list with activity, inspect, create, update, delete."
---

# Automate

You want to manage Home Assistant's automation engine — not just
trigger an action, but durably encode "when X happens, do Y."

## Almost always: discover what already exists

`ha_automation_list` returns automations with their config IDs,
entity_ids, current enabled state, and recent trigger activity
(1h/24h/7d counts plus recent activation deltas):

```json
{
  "limit": 25
}
```

Activity counts are how you spot automations that never fire (likely
broken trigger or stale entity_id in the trigger), or fire too often
(likely a runaway loop or noisy sensor). Read the list before
authoring anything new — duplicate automations are a common
self-inflicted mess in HA.

## Inspect a specific automation

`ha_automation_get` fetches one by config ID or entity_id:

```json
{
  "id": "1700000000"
}
```

Returns the full raw automation object plus registry metadata (area,
labels, aliases, icon). Read this before updating — config updates
are merged shallowly, so you want to know what's there before
modifying.

## Debug a run: ha_automation_traces

When an automation misfires — or right after you create or update one —
`ha_automation_traces` shows what actually happened. Bare (with `id` or
`entity_id`): recent runs newest-first, each with what triggered it,
how it ended (`finished`, `failed_conditions`, `error`), duration, and
any error. Pass a `run_id` from that listing for the full step-by-step
trace in execution order — which trigger fired, each condition's
result, what every action did. HA keeps traces only for a handful of
recent runs, so an empty list usually means "hasn't fired lately," not
"broken." Activity counts from `ha_automation_list` tell you *whether*
it fires; traces tell you *why it did what it did*.

## Author a new automation

Home Assistant (2026.7+) authors automations around *intent*, not
device internals. A purpose-specific trigger describes what you want
to happen — "motion in the office," "a battery got low" — and targets
an area, floor, label, or set of entities. The automation then follows
the home as it changes: move a sensor into the office and the "motion
in the office" trigger picks it up, no re-authoring. Prefer this form.

`ha_automation_vocabulary` lists what a target supports — its purpose
triggers, conditions, and services, in the `domain.name` form the
config takes. Call it first; the vocabulary is per-install because
integrations register their own.

```json
{
  "config": {
    "alias": "Office lights on with motion after dark",
    "description": "When anyone moves in the office after sunset, bring the office lights up — so the room is never dark when occupied.",
    "triggers": [
      { "trigger": "motion.detected", "target": { "area_id": "office" } }
    ],
    "conditions": [
      { "condition": "sun.is_below_horizon" }
    ],
    "actions": [
      { "action": "light.turn_on", "target": { "area_id": "office" } }
    ],
    "mode": "single"
  },
  "metadata": {
    "area_id": "office",
    "label_ids": ["lighting"]
  }
}
```

The purpose trigger targets the *area*, not a specific
`binary_sensor.*`. That is the durable choice — entity-ID triggers
break the moment a device is renamed or replaced.

**2026.7 renames** — use the current names: `battery.became_low`,
`vacuum.returned_to_dock`, `update.became_available`,
`climate.is_target_temperature`.

Classic platform triggers still work and are the right tool when no
purpose trigger fits (a raw MQTT topic, a template, a specific
webhook). Like purpose triggers, each is an entry in `config.triggers`
— never a top-level `trigger` field on the config:

```json
{
  "config": {
    "triggers": [
      { "trigger": "state", "entity_id": "binary_sensor.driveway_motion", "to": "on" }
    ]
  }
}
```

The `config` key holds the raw HA automation object — preserve
HA-native field names. The `metadata` key holds entity registry
overrides; prefer the `_id` variants (`area_id`, `label_ids`,
`category_id`).

**Author it well.** Every automation you create should read like a
careful operator wrote it: a real `alias` and a `description` that
states the intent (not "Automation 7"), an `area_id`, and `label_ids`
where they apply. The metadata is courtesy to whoever opens the HA UI
and context you yourself read back later.

**Before authoring**: resolve real IDs. `ha_automation_vocabulary`
gives you valid trigger/condition/service identifiers for a target;
`ha_registry_search` finds area, label, and entity IDs. Purpose
triggers targeting an area sidestep the classic failure mode — a typo
in an `entity_id` inside a trigger silently breaks the automation:
it registers, returns success, and never fires. After creating, use
`ha_automation_traces` to confirm it does what you intended.

## Update an existing automation

`ha_automation_update` merges config changes shallowly over the
current automation:

```json
{
  "id": "1700000000",
  "config": {
    "mode": "queued"
  }
}
```

The shallow merge means you can change `mode` without re-supplying
triggers and actions. For deeper structural changes (replacing the
trigger array entirely), pass the whole `trigger` key — what you
pass replaces what's there for that key.

Always `ha_automation_get` first if you're changing structure;
otherwise you'll trample fields you didn't mean to touch.

## Retire an automation

`ha_automation_delete` removes it:

```json
{
  "id": "1700000000"
}
```

Verify with `ha_automation_list` afterwards when the deletion
matters. Deleting the wrong automation is recoverable from HA's own
config backups but annoying; double-check the ID first.

## Cross-references

- For one-shot control instead of "encode this rule durably,"
  bounce to `ha_control`. Many "automation" requests are really
  "do this once right now."
- For automations that should run inside a Thane loop instead of
  HA's engine (richer model-driven logic, document outputs,
  multi-step reasoning), bounce to `loops_examples` — `thane_loop_create`
  with `operation="service"` is the alternative when HA's automation
  YAML can't express the judgment you need.
- For HA-event → Thane-loop wake patterns (an automation that
  needs to *tell a Thane loop something happened* rather than act
  itself), the bridge is MQTT: the automation publishes via
  `mqtt.publish`, and the loop registers a wake on the same topic
  via `mqtt_wake_add` (in the `loops` tag). This pairing — HA
  emits, Thane reacts — is the canonical cross-system event hook.
- For inspecting *why* an automation fired or didn't, the activity
  counts on `ha_automation_list` are usually enough; for deeper
  forensics on the events leading up to a trigger, `logs_query`
  (always available) scoped to the relevant time window.
