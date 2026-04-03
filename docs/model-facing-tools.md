# Model-Facing Tools

Read this before adding or changing a tool definition, tool result, or
tool error that will be consumed by a Thane loop. If a tool may be used
by an orchestrator, a delegate, a wake handler, a planning run, or any
other model-driven path, the audience is a model.

This document is a sibling to
[Model-Facing Context](model-facing-context.md). That page is about
context injection and prompt assembly. This page is about tool surfaces
and the data contracts around them.

## Mission

Make the next correct tool call easier.

The model should not have to reverse-engineer the purpose of a tool,
guess which argument is required, infer what a raw identifier means, or
learn a recovery plan from a vague error string. Good tools reduce
decision friction before and after the call.

## Core Principle

Tooling is not a UI.

Our audience is not a human reading pretty output. The goal is compact,
semantic, machine-usable data that helps a model choose the next action
correctly.

Human-readable names still matter, but as semantic metadata for the
model, not as presentation polish.

## Philosophy

### 1. Name tools for decision clarity

Tool names should explain action, object, and scope when ambiguity is
possible.

Good:

- `ha_automation_list`
- `ha_automation_update`
- `macos_calendar_events`
- `add_context_entity`

Avoid:

- overloaded names like `list`, `get`, or `update`
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

Good fit for dual acceptance:

- Home Assistant area IDs or area names
- category IDs or category names
- label IDs or label names

Bad fit:

- friendly names that are likely to be duplicated across a large graph
- values where fuzzy matching would silently guess

When ambiguity exists, fail fast and return the candidates.

### 3. Return compact structure with semantic metadata

Tool results are working memory for later model steps. They should be
compact, structured, and easy to chain into the next call.

Prefer:

- JSON objects and arrays with stable fields
- canonical IDs needed for follow-up mutations
- semantic names alongside opaque IDs when they add meaning
- precomputed relationships, rates, counts, and deltas
- deterministic ordering

Do not add human-oriented prose around data just because it looks nicer.

The purpose of names in results is not friendliness. It is semantic
value. If a model sees only `01JSPY2KHMDFXMSDFXJNKZWX2V`, it may invent a
story. If it also sees `Physical`, it can reason with less guesswork.

### 4. Errors must teach the next move

A delegate should be able to recover from a good tool error in one more
attempt.

Bad errors force the model to speculate:

- `no data found`
- `invalid request`
- `multiple accounts; specify account`

Better errors tell the model what to change next:

- `no calendar events matched account="aimee" between start=... and end=...; try widening the window or removing query`
- `multiple accounts have connected platform providers for macos.calendar/list_events; retry with account set to one of ["aimee", "nugget"]`
- `label "critical" is ambiguous; matches [{"id":"label_a","name":"Critical"},{"id":"label_b","name":"Critical"}]`

An actionable error usually includes:

- which argument needs to change
- the valid choices when the set is small
- whether the failure was ambiguity, absence, gating, or upstream error
- enough echoed context to understand the failed selection

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

Prefer tool families that make the progression obvious:

- `search` or `list`
- `get`
- `create`
- `update`
- `delete`

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

## Conventions

### Prefer JSON when structure matters

Compact JSON is usually the best fit for tool results and validation
errors.

Use prose only when the output is genuinely instructional or when the
tool's job is to write text.

### Keep schemas stable

For model-facing tools:

- keep field names stable
- keep ordering stable unless relevance or time ordering is the point
- cap large outputs
- mark truncation explicitly
- include empty arrays or fixed fields when that makes downstream use
  simpler

### Use names as metadata, not as a replacement for IDs

If a result includes both raw IDs and resolved names, that is usually a
feature, not duplication.

The ID is for exact follow-up calls. The name is for meaning.

### Echo the scoped selection in errors

When an error depends on account, client, label, area, time range, or
query, include those values in the error.

That gives the model something concrete to revise.

### Prefer one-step recovery paths

If an error can suggest a clear next attempt, do it.

Examples:

- tell the model to specify `account`
- tell the model which `client_id` values are available
- tell the model which field was ambiguous
- tell the model whether widening time bounds is likely to help

## Delegate-Specific Guidance

Delegates are literal. They do not have human intuition about what an
error "probably means."

Write tool contracts and errors so a delegate can:

1. understand what failed
2. identify the argument to change
3. issue one better follow-up call

Keep in mind:

- delegates may not have extra narrative context beyond the task and the
  tool response
- delegates are often working under iteration limits and tool timeouts
- delegates benefit from explicit candidate lists more than from generic
  troubleshooting advice

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
7. Is the output compact structure instead of human-oriented narrative?
8. Are we preserving exact upstream state where exactness matters?

If those answers are good, the tool surface is probably on the right
track.
