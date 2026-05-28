---
name: ha
tags: [ha]
kind: trailhead
teaser: "Open for Home Assistant work — reading state, controlling devices, or authoring automations."
next_tags: [ha_observe, ha_control, ha_automate]
---

# Home Assistant

The HA surface is the largest lever this agent has on the physical
world. Reading is cheap and safe; control is fast and stale-ID-prone;
automation authoring is durable and breakable. Match the shape of
the work to one of three branches.

## Choose by what you're doing

- **You want to know what's happening right now** — activate
  `ha_observe`. Single-entity state, fuzzy lookup by description,
  domain enumeration, registry search across areas/labels/devices.

- **You want to change something** — activate `ha_control`. The
  find → act → verify pattern, with safety on stale entity IDs.

- **You want to manage Home Assistant's own automations** — activate
  `ha_automate`. List with activity stats, inspect, create, update,
  delete.

## The constants across all three branches

- **`ha_call_service` does not validate entity IDs.** A typo or stale
  ID returns success and silently does nothing. This is the single
  most consequential gotcha in the HA surface; every action-shaped
  branch carries the verify-after pattern.
- **`ha_control_device` is the high-level path; `ha_call_service` is the
  low-level path.** Use `ha_control_device` unless you already have the
  exact entity_id from a recent lookup *in the same turn*.
- **Sustained attention is `awareness`'s job, not `ha`'s.** A one-off
  state check uses `ha_observe`; a loop watching a room subscribes
  via `awareness` and lets entity state stay current between turns.
- **Delivery and escalation are `notifications`'s job.** When the
  next move is "tell someone about this," activate notifications;
  HA's tools are about state and control, not interruption.

---
name: ha_observe
tags: [ha_observe]
kind: trailhead
teaser: "Read current state — single entity, fuzzy lookup, domain enum, or registry search."
---

# Observe

You want to know what's happening. Four tools, picked by how
specifically you can name what you're looking for.

## I know the exact entity_id

`ha_get_state` returns the current state and attributes:

```json
{
  "entity_id": "light.office_main"
}
```

The fastest path when the entity_id is already in hand. Returns the
state value plus all attributes (brightness, color, last_changed,
etc.).

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

## I know the description but not the entity_id

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

## I want everything in a domain

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
entities by their live *state* (what's on, what's open, low batteries),
reach for `ha_search_states` instead.

## I want everything matching a live-state condition

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

## I want everything in a room

`get_area_activity` is the whole-area perception view — the native
answer to "what's in the office" or "is everything okay in the
kitchen":

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
pass `include_hidden` or `include_diagnostic` for a forensic pass. Reach
for this instead of listing a domain and cross-referencing rooms by
hand.

## I want richer search across the registry

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

`ha_call_service` requires the exact entity_id:

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

## Author a new automation

`ha_automation_create` takes a full HA automation object:

```json
{
  "config": {
    "alias": "Driveway camera notification",
    "description": "Notify when the driveway camera sees motion at night",
    "trigger": [
      {
        "platform": "state",
        "entity_id": "binary_sensor.driveway_motion",
        "to": "on"
      }
    ],
    "condition": [
      {
        "condition": "sun",
        "after": "sunset",
        "before": "sunrise"
      }
    ],
    "action": [
      {
        "service": "notify.mobile_app_pixel",
        "data": {
          "message": "Driveway motion detected"
        }
      }
    ],
    "mode": "single"
  },
  "metadata": {
    "area_id": "driveway",
    "label_ids": ["security"]
  }
}
```

The `config` key holds the raw HA automation object — preserve
HA-native field names (alias, description, trigger, condition,
action, mode). The `metadata` key holds entity registry overrides;
prefer the `_id` suffixed variants (`area_id`, `label_ids`,
`category_id`) over their friendly-name siblings.

**Before authoring**: use `ha_registry_search` to find real area
IDs, label IDs, and entity IDs. Don't guess — typos in entity_ids
inside a trigger silently break the automation the same way they
silently break `ha_call_service`. The automation will register, return
success, and never fire.

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
  multi-step reasoning), bounce to `loops_examples` — `thane_curate`
  is the alternative when HA's automation YAML can't express the
  judgment you need.
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
