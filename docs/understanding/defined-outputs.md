# Defined Outputs

A loop that produces something durable declares it. The declaration is
the contract: it names what the loop owns, generates the only tool the
loop may write through, and puts the current state of that output into
every iteration's context.

```yaml
outputs:
  - name: metacognitive_state
    type: maintained_document
    ref: core:metacognitive.md
  - name: watch_status
    type: structured_payload
    target: apple_watch.rectangular
    ref: mqtt:watch_status
```

Those two entries generate `replace_output_metacognitive_state` and
`set_output_watch_status`. Both are request-scoped — they exist only for
that loop's runs, never in the global tool registry — so a loop's write
surface is exactly as wide as what it declared.

## Tiers

Outputs come in tiers, chosen by who reads the result.

| Type | Mode | Tool | For |
|---|---|---|---|
| `maintained_document` | `replace` | `replace_output_*` | A reader who wants the whole picture, rewritten each cycle |
| `journal_document` | `append` | `append_output_*` | A reader who wants the history, appended to and never rewritten |
| `structured_payload` | `set` | `set_output_*` | A *display* with fixed geometry and no room for prose |

The document tiers write markdown into a managed document root, where
path safety, indexing, provenance, and root policy already live. The
structured tier writes named slot values to a rendering surface.

## Structured payloads

A display is not a document. An Apple Watch complication has four lines
of perhaps twenty characters each, a gauge that wants a number between
zero and one, and no capacity for a paragraph that got a little long. So
the structured tier does not ask the model for text — it asks for slots.

A `structured_payload` output names a **target**: an entry in the target
registry (`internal/model/outputtargets`) describing one renderable
surface. The target defines the slots, their types, their size budgets,
and which slot is primary. That definition becomes three things at once:

- the JSON schema the generated `set_output_*` tool advertises, so the
  model sees `title (string, max 18 characters)` rather than a free-text
  field;
- the validator the handler runs, which **rejects** an over-budget or
  malformed value with an error naming the slot and the limit;
- the slot contract rendered into the loop's context each iteration,
  alongside whatever was last published.

Rejection is deliberate. A truncated string on a watch face is an
unreadable fragment with no indication that anything was dropped, and a
loop that never learns its budgets will overrun them forever. The only
values the layer changes are surrounding whitespace and color hex casing.

Every call replaces the whole payload. A slot the model omits is
cleared, because the surface shows exactly the last payload — there is no
partial-update form for a thing with no history.

### Registered targets

| Target | Surface |
|---|---|
| `apple_watch.rectangular` | The four-line `accessoryRectangular` complication: the large middle slot of the Modular Ultra face and the wide slot on Modular. Slots: `value`, `title`, `subtitle`, `bottom_text`, `fraction`, plus gauge/icon/text colors |
| `apple_watch.circular` | The small round `accessoryCircular` complication: a gauge ring around a compact center. Slots: `value`, `title`, `fraction`, plus gauge/icon/text colors |

Adding a target is adding a `Target` value to that package. Targets live
in code rather than config so their budgets and validators are
compile-checked and covered by tests; nothing in the loop layer, the tool
generation path, or the sink changes when a new one lands.

## Where the payload goes

Structured payloads publish over MQTT as Home Assistant discovery
sensors. The `ref` names the sink and the entity suffix:

```yaml
ref: mqtt:watch_status     # → sensor.<device_name>_watch_status
```

The primary slot becomes the sensor's **state**; every other slot becomes
a **JSON attribute**. That split is what keeps the downstream binding
trivial — the headline value needs no attribute lookup.

MQTT must be configured. A loop that declares a structured payload
without it fails hydration with a configuration error rather than
starting up with a tool that could never publish.

### Binding a watch complication

With `sensor.thane_watch_status` published, the Home Assistant companion
app on iOS renders it:

1. Settings → Companion app → Apple Watch → Complications.
2. Add a complication, choose the size matching your target
   (Rectangular or Circular), and choose an **Entity** source pointing at
   the sensor.
3. Bind the slots: `{value}` reads the state, `{attr:title}`,
   `{attr:subtitle}`, and `{attr:bottom_text}` read the attributes.
4. For the gauge or progress bar, set minimum `0` and maximum `1` so it
   consumes `{attr:fraction}` directly.

An entity-sourced complication refreshes on the watch itself, so the
readout stays current without the phone in range. It cannot template its
colors, though — for those, use a template-sourced complication whose
color fields read the same attributes:
`{{ state_attr('sensor.thane_watch_status', 'gauge_color') }}`.

Colors are best-effort by nature: watchOS renders on-face complications
in the watch face's own accent palette, so expect full fidelity in the
Smart Stack and a tinted approximation on the face itself. The gauge's
*fill level* is the reliable signal there, not its hue.

## What the loop sees

Every iteration gets a **Declared Durable Outputs** context block. For a
document output it carries the current body (truncated with an explicit
marker when it exceeds the budget). For a structured payload it carries
the target's full slot contract — names, kinds, budgets, descriptions —
the entity ID, and the last payload this process published, with a
freshness delta.

The contract is repeated in full each iteration on purpose. The model is
choosing values against fixed geometry, and a budget it cannot see is a
budget it will overrun.

When nothing has been published yet, the block says so explicitly and
notes that the surface may still be showing a payload from before the
last restart. The last-published record is in-process only; MQTT retains
the payload on the broker, so a restarted Thane has a display it has not
written to yet.

## See also

- [Document Roots](document-roots.md) — where document-tier outputs land
- [The Agent Loop](agent-loop.md) — how loops run and self-pace
- [Model-Facing Tools](../model-facing-tools.md) — why runtime-scoped
  tools stay runtime-scoped
