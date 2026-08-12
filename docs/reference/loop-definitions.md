# Loop Definitions

A loop definition describes one loop — its identity, prompt shape,
pacing, outputs, and watches. One spec schema underlies every way a
definition comes to exist: the definition documents in a core root,
the `loops.definitions` list in config.yaml, and the specs the agent
authors itself through `thane_loop_create` and `loop_definition_set`.
This page documents that schema.

A definition document is self-contained by design — nothing in it is
specific to one install except the entities it watches — so a document
written for one Thane can run on another. The shipped documents in
[`loops/`](../../loops/) are reference implementations of the form:
embedded into the binary as defaults, overridden by a document of the
same name in `<core>/loops/`, and re-read at every boot.

## The document form

A definition document is markdown: one fenced yaml block carries the
spec, and prose sections carry the prompts. Three H2 headings are
reserved; every other heading is ordinary prose the parser ignores.
The parser is strict on purpose — an unknown or misspelled key refuses
the boot loudly rather than silently doing nothing. Run
`thane validate` after editing — it parses the config and every
definition document, reporting all problems at once — before
restarting. (Specs authored through the loop tools get the same
validation from `loop_definition_lint` before anything persists.)

| Section | Carries |
|---|---|
| `## Spec` | One fenced yaml block — the keys documented below |
| `## Task` | The per-iteration prompt (the spec key `task` exists, but a document declaring both is refused — the section is the home for prose) |
| `## Supervisor Review` | Extra instructions prepended on supervisor turns — the prose home for `supervisor_profile.instructions`, same both-declared-is-refused rule |

## Identity and placement

| Key | Meaning |
|---|---|
| `name` | Unique definition name; required. Everything downstream keys on it |
| `intent` | One-sentence statement of why the loop exists; surfaced by the loop tools. Distinct from the task, which says what to do each wake |
| `enabled` | Whether the definition auto-starts under runtime lifecycle management |
| `parent_name` | Container to nest under, by name; the loop inherits the container's tags and subscriptions at launch. Omit for top-level |
| `parent_id` | Runtime-assigned at launch; never authored — use `parent_name` |
| `metadata` | Opaque string-to-string tags for correlation and audit; never interpreted as configuration |

## Prompt shape

| Key | Meaning |
|---|---|
| `prompt_mode` | `full` (default) wears the complete identity — persona, ego, axioms, always-on context. `task` is the compact worker prompt for mechanical maintainer/watcher/poller loops; it keeps tagged guidance, the loop's declared subscriptions and self view, the task, and current conditions while shedding the identity. Loops that reflect on the agent or speak in its voice stay `full` |
| `profile` | Routing and request shaping: `model` (pin a model), `quality_floor` (minimum model rating 1–10), `mission` (context profile), `local_only` / `prefer_speed` (`"true"`/`"false"` strings), `delegation_gating` (`disabled` gives direct tool access), `exclude_tools`, `instructions` (self-only text prepended to every iteration's task), `extra_hints` (free-form routing hints) |

## Operation and completion

| Key | Meaning |
|---|---|
| `operation` | `service` = perpetual, self-paced within the sleep envelope; `event_driven` = quiescent, wakes only on triggers (no timer fields allowed); `background_task` = detached one-shot; `request_reply` = synchronous one-shot; `container` = non-executing grouping node (execution-shaped keys are refused) |
| `completion` | Where a result is delivered: `none` (the service-loop default), `return`, `conversation`, or `channel`. Mainly for one-shot operations |

## Pacing and lifetime

| Key | Meaning |
|---|---|
| `sleep_min` | Tightest interval between iterations (Go duration, e.g. `15m`); `set_next_sleep` can never wake sooner |
| `sleep_max` | Loosest interval (e.g. `12h`) |
| `sleep_default` | Initial sleep before the loop self-adjusts; must sit inside the envelope |
| `jitter` | Sleep randomization factor in [0, 1]; `0.2` varies sleep by ±20% |
| `max_duration` | Wall-clock lifetime cap (Go duration); omit for unbounded |
| `max_iter` | Lifetime iteration cap; omit for unbounded |
| `on_retrigger` | Behavior when triggered while already running: `single` (ignore), `restart`, `queue`, `spawn` |
| `conditions` | Eligibility constraints — a `schedule` with `timezone` and day/time `windows`; empty means always eligible |

## Outputs

| Key | Meaning |
|---|---|
| `outputs` | Durable documents the loop maintains through generated tools. Entry keys: `name`, `type` (`maintained_document`, or `working_notes` for the loop-private variant), `ref` (managed document ref like `self:ego.md`), `mode` (`replace`), `purpose` (model-facing guidance), `facets` (`status_line` / `teaser` / `digest` projections; declaring any swaps the write tool to `publish_output_*`), `audience` (`published` or `internal`) |

## Watching

| Key | Meaning |
|---|---|
| `subscriptions` | Entities rendered into context every iteration. Entry keys: `entity_id` (an id, glob, or `area:`/`label:`/`floor:` target), `history` (context windows in seconds), `forecast` (for `weather.*` entities), `ttl_seconds`, `mode` (`render` / `ingest` / `both`), `self_only` (containers: keep out of descendants), `requires_tag`, `transitions` and `transitions_window_seconds` (recent state-change log), `wake` and `wake_debounce_seconds` (wake the loop on change, debounced) |

## Supervisor turns

| Key | Meaning |
|---|---|
| `supervisor` | Enable the per-wake Bernoulli trial that promotes an iteration to a more capable model |
| `supervisor_prob` | Probability of a supervisor turn per wake, in [0, 1] |
| `supervisor_profile` | Overlay applied on supervisor turns — same keys as `profile`; set fields win, unset fields fall back. Its `instructions` are carried by the `## Supervisor Review` section |

## Tools and routing

| Key | Meaning |
|---|---|
| `tags` | Capability tags activated at iteration 0: tool surface, tagged knowledge, tagged context |
| `exclude_tools` | Tool names to deny. The token `group:direct_human_egress` expands to every direct human-messaging tool, so the list cannot go stale as tools are added |
| `routing_factors` | Open-ended string map of router scoring inputs; prefer the named `profile` fields for well-known knobs |
| `delegation_gating` | Top-level form of the same switch as `profile.delegation_gating`; prefer the profile form |
