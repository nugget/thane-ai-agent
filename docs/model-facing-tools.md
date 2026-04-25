# Model-Facing Tools

Read this before adding or changing a tool definition, tool result, or
tool error that will be consumed by a Thane loop. If a tool may be used
by an orchestrator, a delegate, a wake handler, a planning run, or any
other model-driven path, the audience is a model.

This document is a sibling to
[Model-Facing Context](model-facing-context.md). That page is about
context injection and prompt assembly. This page is about tool surfaces
and the data contracts around them. Assume the conventions in the
companion document still apply here. This page covers the tool-specific
rules that are easiest to get wrong.

## Philosophy

### 0. Tool calling is a runtime contract, not just a prompt

Tool use has to survive three layers:

- the semantic tool catalog
- the prompt/tool advertisement rendered for the model
- the provider/model runtime that turns model output into actual tool calls

Do not assume every model family emits native tool-call structures just
because one premium model does. Some local/open models emit raw JSON or
fenced pseudo-tool payloads instead. Thane should centralize those
compatibility rules instead of scattering parser heuristics across
providers.

When model families differ:

- keep one semantic tool catalog
- add a shared model-family adapter/profile layer
- centralize raw-text tool-call recovery in one runtime seam
- vary prompt-side tool contracts only where the model family actually
  needs it

### 1. Name tools for decision clarity

Tool names should explain action, object, and scope when ambiguity is
possible.

Good:

- `ha_automation_list`
- `ha_automation_update`
- `ha_registry_search`
- `add_context_entity`

If platform or source matters, keep it in the name, but do not drop the
verb to make room for it. Prefer names like
`macos_calendar_list_events` over names like `macos_calendar_events`.

Avoid:

- bare generic names like `list`, `get`, or `update` with no object or
  scope
- synonyms that hide the true object being manipulated
- names that require knowledge of internal package structure

If two tools would be hard to distinguish from the name alone, the names
are not done yet.

### 2. Accept how models think, then canonicalize

Models often reason in names, not IDs. Upstreams often require IDs, not
names.

When deterministic resolution is possible and ambiguity is bounded,
tools should accept either human names or canonical IDs, then normalize
immediately to the canonical form before making the upstream call.

Example:

- accept `Kitchen` or `area_kitchen_1`
- resolve both to the canonical area ID before calling upstream
- if resolution fails, return a structured not-found or ambiguity error
  instead of guessing

Good fit for dual acceptance:

- Home Assistant area IDs or area names
- category IDs or category names
- label IDs or label names

Bad fit:

- friendly names that are likely to be duplicated across a large graph
- values where fuzzy matching would silently guess

When ambiguity exists, fail fast and return the candidates.

### 3. Return semantic metadata, not just raw IDs

Tool results should include the canonical IDs needed for follow-up calls
and the names or labels needed for interpretation.

The purpose of names in results is not friendliness. It is semantic
value. If a model sees only a category ID like
`01JSPY2KHMDFXMSDFXJNKZWX2V`, it may invent a story about what that
category means. If it also sees the resolved category name
`Door Locks`, it can reason with less guesswork.

### 4. Errors must teach the next move

A delegate should be able to recover from a good tool error in one more
attempt.

Bad errors force the model to speculate:

- `no data found`
- `invalid request`
- `multiple accounts; specify account`

Better errors tell the model what to change next:

- `no calendar events matched account="aimee" between start=... and end=...; try widening the window or removing query`
- `multiple accounts have connected companion apps for macos.calendar/list_events; retry with account set to one of ["aimee", "nugget"]`
- `label "critical" is ambiguous; matches [{"id":"label_a","name":"Critical"},{"id":"label_b","name":"Critical"}]`

An actionable error usually includes:

- which argument needs to change
- the valid choices when the set is small
- whether the failure was ambiguity, absence, gating, or upstream error
- enough echoed context to understand the failed selection

### Multi-account ambiguity

If a tool can target multiple accounts, clients, or tenants, ambiguity
errors should name the parameter to supply and list the valid choices
when the set is small.

Good:

- `multiple accounts have connected companion apps for macos.calendar/list_events; retry with account set to one of ["aimee", "nugget"]`

When the result shape allows it, include the choices as structured data
as well as in the error text.

### 5. Distinguish empty results from selection problems

`No data` is not one thing.

These are meaningfully different:

- the selection was ambiguous
- the target could not be found
- the query was valid but returned zero results
- the upstream was unavailable
- the tool was gated or unavailable in this loop

Do not collapse them into the same string.

Models can recover from a precise empty result. They struggle when every
miss looks identical.

### 6. Separate discovery from mutation

Models work best when they can inspect before they change.

Prefer tool families that make the progression obvious through
descriptive names:

- `*_search` or `*_list`
- `*_get`
- `*_create`
- `*_update`
- `*_delete`

Mutation tools should return enough canonical data for the next step:

- created or updated IDs
- normalized metadata
- enabled or disabled state
- a compact summary of what changed

If the upstream supports validation or dry-run behavior, expose it.

### 7. Preserve exactness at the system boundary

Human names and semantic labels are metadata. They are not the source of
truth.

When writing back to upstream systems:

- use canonical IDs after normalization
- keep raw configs faithful when the point is exact system state
- avoid inventing a Thane-only schema when the upstream object is
  already the right abstraction

This keeps the model-friendly layer separate from the machine-authority
layer.

### 8. Make ambiguity explicit and bounded

If multiple targets are plausible, say so and enumerate them when the
set is small.

Good:

- return the two matching accounts
- return the matching label IDs and names
- return the matching entities with area and domain

Avoid silent best guesses unless the tool contract explicitly promises a
deterministic winner.

### 9. Design for chaining

The output of one tool should naturally support the input of the next.

Examples:

- list results include IDs needed for get or update
- search results include names that explain opaque IDs
- activity summaries include enough context to decide whether to inspect
  a specific item
- validation results point at the exact field or path that failed

If the model has to re-run a broad search just to recover the argument
for a follow-up call, the first tool did not expose enough.

### 10. Prefer exact machine-readable tool contracts

When the model needs generated information about available tools,
capabilities, or activation affordances, default to compact structured
data over narrative explanation.

The model should be able to tell:

- which tools are actually callable
- which capabilities are loaded vs merely available
- which exact tool names are valid
- which recovery path applies if a tool family uses raw-text fallback

Do not hide those distinctions in prose if a stable JSON shape would
make them obvious.

## Common Pitfalls

### Parameter type mismatches

If a tool expects an integer, path, enum, or boolean, say so explicitly
in the schema and description. Do not assume the model will infer the
type from the parameter name alone.

Bad:

- `forge_issue_get(number: "352")`

Better:

- `forge_issue_get(number: 352)`

### Path discovery spirals

Delegates burn iterations when a tool design forces a discovery step for
something that could have been accepted literally.

Prefer tool shapes that accept exact file paths, IDs, or other canonical
selectors directly when the caller already has them.

Stable path abstractions are also valid when they resolve
deterministically before execution. This is the lesson of
[#377](https://github.com/nugget/thane-ai-agent/issues/377) and
[PR #378](https://github.com/nugget/thane-ai-agent/pull/378): prefixes
like `kb:`, `scratchpad:`, and `temp:` are good abstractions when they
are backed by a shared resolver and collapse to concrete paths without
making the model search the filesystem.

Bad:

- requiring a search step before every file read or entity mutation

Better:

- `read_file(path="/home/thane/config.yaml")`
- `read_file(path="kb:reference/architecture.md")`
- `ha_automation_get(entity_id="automation.low_battery_warning")`

## Delegate-Specific Guidance

Delegates are literal. They do not have human intuition about what an
error "probably means."

Write tool contracts and errors so a delegate can:

1. understand what failed
2. identify the argument to change
3. issue one better follow-up call

Tool descriptions should help with that. Include:

- required parameter names and types when they are easy to misuse
- what the tool returns when that is important for the next step
- literal examples when a value shape is non-obvious
- common failure modes when there is a known trap

For example, if a tool expects an integer issue number, say `number
(integer, not string)` instead of just `number`.

If a delegate would need to ask itself "what do I try next?", the tool
surface can probably be improved.

## Review Checklist

Before merging a new or changed tool, ask:

1. From the tool name alone, can a model tell what it acts on?
2. Are the parameters shaped the way a model is likely to think about
   the problem?
3. Does the result include the canonical IDs needed for the next step?
4. Does it also include the semantic names needed for interpretation?
5. Are ambiguity and empty-result cases distinguished cleanly?
6. Could a delegate recover from the error in one more attempt?
7. Does the description call out parameter types where misuse is likely?
8. Would multi-account or multi-target ambiguity be obvious to recover?
9. Is the output compact structure instead of human-oriented narrative?
10. Are we preserving exact upstream state where exactness matters?

If those answers are good, the tool surface is probably on the right
track.
