# Model-Facing Context

Read this before changing code that emits text or data for later model
consumption. It keeps reusable lessons out of scattered issues and
inside project memory. If an output may become system prompt content,
capability context, delegate bootstrap context, tool output, summary
scaffolding, or any other loop input, the audience is a model.

## Mission

Make the next correct model action easier.

Do not make the model infer what Go can state directly. Do not make it
do timestamp math, recover hidden defaults, guess scope from vague
names, or extract a schema from narrative prose when a compact
structure would be clearer.

Prefer explicit naming, precomputed relationships, stable schemas,
capability-scoped context, and delta-oriented time. When a new issue
teaches a reusable lesson, put it here.

## Core Principle

Typing is free. Cognitive clarity is expensive.

Humans tolerate terse names, implied structure, and narrative glue.
Models do better when structure is obvious, relationships are
precomputed, and interpretation work is removed.

## Philosophy

### 1. The model is the audience

Prefer explicitness over terseness. Name things so a model can choose
the right tool, field, or section without guessing. This is the lesson
of [#274](https://github.com/nugget/thane-ai-agent/issues/274): optimize
for decision clarity, not typing efficiency.

### 2. Offload cognition to Go

If Go can derive, normalize, sort, annotate, or disambiguate something,
it should. Do not spend model attention on arithmetic, unit conversion,
schema inference, or hidden defaults.

This is the lesson of
[#458](https://github.com/nugget/thane-ai-agent/issues/458): do the time
math before the model sees the data.

### 3. Live context beats static prose

If a fact depends on current config, active capabilities, recent tool
activity, or external state, build it dynamically. Do not freeze live
operational context into a markdown file and expect the model to recover
the missing state on its own.

This is the lesson of
[#500](https://github.com/nugget/thane-ai-agent/issues/500): inject the
context the delegate needs instead of making it guess.

### 4. Structure beats rhetoric

For operational data, compact structure usually beats paragraph prose.
Use prose for behavioral guidance and framing. Use data structures for
facts, events, entities, and state.

### 5. Generated runtime data defaults to JSON

If Go is generating runtime state for later model consumption, the
default shape is typed JSON, not markdown prose.

Use markdown for:

- section boundaries
- brief framing notes
- normative instructions

Use JSON for:

- live operational state
- capability catalogs and loaded state
- environment data
- event projections
- any payload we may later diff, cache, trim, or suppress by policy

Do not make this a per-call stylistic decision. JSON is the default for
generated model-facing data unless prose is genuinely the clearer
contract.

### 6. Model families get adapters, not one universal prompt

Different model families do not interpret the same context and tool
surfaces equally well. Claude-style tool use, OpenAI-style structured
tool use, and local open-model raw-text tool use are different runtime
contracts.

When behavior differs meaningfully by family or provider:

- add a shared adapter/profile layer
- keep the semantic source of truth shared
- vary the rendered contract or recovery behavior by model family

Do not assume a prompt that evolved around one premium model will
transfer cleanly to a broader registry.

## Conventions

### Choose the simplest shape that removes ambiguity

Use:

- compact JSON objects for single records with stable fields
- compact JSON arrays for homogeneous lists
- one JSON object per line for event streams or append-only summaries
- markdown headings and short notes for section boundaries or human-scale
  framing
- prose only when the content is genuinely instructional

Generated runtime context should look like environment data, not like
part of the active conversation.

Avoid turning structured state into narrative just because it reads more
nicely to humans.

### Prefer explicit names

Names should explain action, object, and scope when ambiguity is
possible.

Good:

- `execute_shell_command`
- `list_home_assistant_entities_by_domain`
- `default_owner`
- `recent_operations`

Avoid:

- abbreviations that require domain inference
- Unix jargon when a plain name exists
- short keys unless they are already obvious in a tight schema

Short keys are acceptable only when the schema is tiny and the meaning is
still immediate. Clarity is the default.

### Pre-compute relationships

Do not force the model to infer relationships that Go already knows.

Prefer:

- normalized values instead of raw encodings
- explicit defaults instead of implied fallback behavior
- pre-joined related fields instead of requiring cross-reference lookup
- capability-scoped context instead of global dumps
- derived summaries when they remove multi-step reasoning

### Use delta-oriented time for recency-sensitive context

Models are bad at timestamp arithmetic. For context intended to drive
reasoning:

- past events should usually be shown as deltas
- future events should keep the absolute time only when wall-clock
  anchoring matters, and should still include a delta
- persistent storage, logs, and APIs should keep absolute timestamps

Use the shared helpers in `internal/awareness/timefmt.go`:

- `FormatDelta`
- `FormatDeltaOnly`
- `ParseTimeOrDelta`

When a tool naturally wants relative scheduling, accept delta syntax as
input instead of forcing the model to invent RFC3339 timestamps.

### Keep schemas stable and deterministic

For model-facing data:

- sort deterministically unless temporal order is the point
- put newest or most actionable items first when ordering by relevance
- keep field names and section names stable across calls
- cap large outputs
- mark truncation explicitly instead of silently dropping context
- emit empty arrays or fixed fields when schema stability matters more
  than byte savings

Determinism helps the model compare turns without relearning the format.

### Scope context to the capability that needs it

Do not inject every operational detail into every session. If a context
block only matters when a capability tag is active, it belongs behind a
tag-scoped provider or tagged knowledge article.

This keeps prompts lean and makes available context more meaningful.

### Separate instructions from data

Behavioral guidance belongs in talents and prompts. Runtime facts belong
in context providers. Historical conversation data belongs in a clearly
delimited data block.

Do not hide instructions inside what claims to be data.

### Human readability is secondary

Use brief framing when it removes ambiguity. Once the shape is clear,
prefer compactness.

## Anti-Patterns

- Human-optimized terse names that make the model infer purpose
- Essay-like markdown for generated operational state that could have
  been a compact schema
- Raw absolute timestamps in recency-sensitive context
- Static markdown files for live operational state
- Dumping raw upstream payloads when a smaller projection would do
- Making the model derive defaults, availability, or scope from scattered
  clues
- Presenting the same fact in multiple conflicting shapes
- Silent truncation or unstable ordering

## Placement And Ownership

- `internal/agent` owns assembly, section ordering, and capability-gated
  injection
- domain packages own projection from raw domain state into model-facing
  views
- `internal/awareness` owns shared time and recency helpers plus other
  cross-domain cognition aids
- talents and prompt builders own behavioral instruction, not runtime data

If a formatting rule is reused across multiple domains, it should move
toward a shared helper instead of being reimplemented ad hoc.

## Likely Consolidation Targets

Likely shared seams:

- section builders for "heading + optional note + compact data"
- shared ordering and truncation helpers for model-facing lists
- a single home for time policy and relative-time parsing
- common projections for recurring shapes like recent operations, recent
  events, and capability-scoped configuration
- stricter review of field naming so schemas drift less over time

We should consolidate when the same formatting decision appears in
multiple packages, not before.

## Litmus Test

Before adding or changing model-facing context, ask:

1. What work is the model still being forced to do that Go could do first?
2. Is this shape optimized for a model, or only for a human maintainer?
3. Does this belong in always-on context, capability context, a tool
   result, or nowhere at all?
4. If this data changes often, why is it static?

If those questions are answered well, the formatting is probably on the
right track.

## Current Reference Points

Good places to look for existing patterns:

- `internal/promptfmt/timefmt.go`
- `internal/awareness/entity_format.go`
- `internal/agent/tag_context.go`
- `internal/forge/context.go`
- `internal/notifications/history_provider.go`
- `internal/agent/channel_provider.go`

## Related

- [`anthropic-caching.md`](anthropic-caching.md) — how the system
  prompt sections map onto Anthropic cache TTLs, and the decision
  tree for adding a new section without breaking the cache.
