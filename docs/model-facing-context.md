# Model-Facing Context

Read this before changing code that emits text or data for later model
consumption. It keeps reusable lessons out of scattered issues and
inside project memory. If an output may become system prompt content,
typed context buckets, delegate bootstrap context, tool output, summary
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

- `ha_registry_search`
- `archive_session_transcript`
- `default_owner`
- `recent_operations`

Avoid:

- abbreviations that require domain inference
- Unix jargon when a plain name exists
- short keys unless they are already obvious in a tight schema

Short keys are acceptable only when the schema is tiny and the meaning is
still immediate. Clarity is the default.

### Write from the model's vantage point

The model is not reading a file on disk. It is receiving context that it
may incorporate into its current self-understanding, decision process, or
tool plan.

Use language whose referents are clear from that vantage point:

- "These words are from past you" when framing talents
- "When talking with Nugget on Signal..." when framing channel context
- "Current loaded capabilities" when naming runtime state

Avoid loose deictic language such as "this is how..." or "this document
explains..." unless the immediately preceding noun makes the referent
unambiguous. Editor-facing language may feel natural while authoring a
markdown file, but it forces the model to infer whether "this" means the
file, the section, the capability, the relationship, the channel, or the
current conversation.

Keep tense and perspective stable inside a block. Historical memory
should read as past context. Runtime state should read as current state.
Behavioral guidance should address the model directly.

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

Use the shared helpers in `internal/model/promptfmt/timefmt.go`:

- `FormatDelta`
- `FormatDeltaOnly`
- `ParseTimeOrDelta`

When a tool naturally wants relative scheduling, accept delta syntax as
input instead of forcing the model to invent RFC3339 timestamps.

Curated prose gets the same treatment through temporal templates:
`{{delta:2026-09-18}}` (bare date or RFC3339 value) expands at read
time via `promptfmt.ExpandTemporalTemplates` — day words for a date
("today", "+20d"), a signed compact delta for an instant ("+3d16h") —
so an authored document stays true between rewrites. Only reader
surfaces expand (tagged-article injection today); author surfaces
(`doc_read`, the publish tools, git) keep the raw template so the
round-trip is byte-exact, and a malformed template renders verbatim
rather than disappearing. Templates render values, never claims —
prose whose truth changes with data is a wake concern, not a
substitution concern (#1431).

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

### Separate metadata from literal corpus

When replaying stored conversation history, keep curation metadata outside
the literal message body. Provider-native `messages[]` roles carry the
speaker role; do not repeat `role=user` or `role=assistant` inside the
content unless the original stored role differs from the provider role.

Use an explicit boundary around the corpus:

```text
[stored conversation history; age_delta=-120s; channel=signal]
<conversation_message>
literal message text
</conversation_message>
```

The same projection rules should back live stored-history messages and
archive-derived JSON (`archive_search`, `archive_range`, and
`archive_session_transcript`): known transport envelopes such as Signal
sender/timestamp headers become metadata rather than corpus. Live
provider `messages[]` should carry only the lightweight metadata needed
for context; archive JSON can preserve fuller provenance fields for
search and debugging. In both cases, `content` remains the literal
conversation text.

### Scope context to the capability that needs it

Do not inject every operational detail into every session. If a context
block only matters when a capability tag is active, it belongs behind a
tag-scoped provider or tagged knowledge article.

This keeps prompts lean and makes available context more meaningful.

### Keep loop-declared outputs concrete

If a loop declares durable outputs, render those outputs from the same
runtime spec that generates the tools. The context should state the
output name, kind, document reference, generated tool name, write mode,
delta-formatted freshness, and current content or recent journal tail in
a capped, deterministic shape.

This is runtime data. Put it in a context provider block, not in static
prompt prose, and do not make the model infer whether a file tool,
document tool, or generated output tool is the intended write surface.

### Separate instructions from data

Behavioral guidance belongs in talents and prompts. Runtime facts belong
in context providers. Historical conversation turns belong in role-native
chat messages rather than the root system prompt. Generated summaries or
handoffs that are stored as conversation memory should be labeled as
historical context, not promoted to active system instructions.

Do not hide instructions inside what claims to be data.

### Shape instructions by the job each form does

For instruction documents a model executes — loop mandates, delegate
briefs, supervisor overlays — the lesson of the #1341 mandate audits is
that structure is not one choice but several, and each part of the
document has a form that serves a model best:

- **Identity and stance are prose.** Who the reader is, what the
  document is for, how to hold the work — short, reason-carrying
  paragraphs. The connective tissue is the content.
- **Procedure is a numbered list.** A turn protocol is a checklist;
  sequence and completeness are the information, and models execute an
  explicit spine more reliably than the same protocol dissolved into
  paragraphs.
- **Constraints live at their point of application, not in a rules
  section.** A "Guidelines" section far from the steps is a join the
  model must perform — recall the rule while executing the step — and
  it is where rules go stale unnoticed. Put the truncation guard inside
  the read step, the timestamp rule inside the write step, the toolkit
  truth beside the tools' first mention.
- **Flat lists equalize weight.** A hazard and a style preference in
  the same bullet list get roughly uniform treatment under context
  pressure. Spend emphasis in prose: say which constraint outranks the
  others, and why.
- **Category boundaries are short example lists closed by one
  principle.** The principle is the carrier and the generalizer; the
  bullets are its examples ("if it reads like something you'd put in a
  ticket, it doesn't belong here").
- **Attach reasons to rules.** A bare imperative invites literal
  compliance or quiet neglect; a rule with its why is a compressed
  policy the model can re-derive in situations the author never
  foresaw.

The visual affordances lists provide humans — symmetry, scannability,
bold parallel grammar — are cosmetic to a model, which reads every
token. What matters instead: unambiguous boundaries, explicit priority,
stable anchors for self-reference, and reasons. This is the
instruction-document counterpart of the "cafeteria bullets" warning in
[model-facing-tools.md](model-facing-tools.md).

### Human readability is secondary

Use brief framing when it removes ambiguity. Once the shape is clear,
prefer compactness.

## Context Advertisements

Generated context is an attention market. A subsystem should offer what it
has that matches the current request, then let one final discriminator rank,
filter, and limit the combined set. It should not eagerly inject a payload or
declare its own global importance.

A context advertisement carries:

- stable source identity and an optional document ref
- the typed prompt bucket where selected content belongs
- request-relative match evidence such as exact subject, alias, semantic,
  lexical, or ambient relevance
- the projections available for materialization, including their role,
  format, and conservative byte estimate

Projection roles describe what a consumer gets:

- `signal` is the compact outward-facing reason to spend attention. A
  document's `status_line` and `teaser` are different authored shapes with
  this same role. Whether the signal is ambient or request-matched belongs to
  the advertisement's evidence, not its prose.
- `context` carries enough substance to use directly. A document `digest` is
  the canonical form.
- `detail` is the complete source. It is reached through an explicit read or
  escalation, never selected automatically.

Go owns evidence ordering, deduplication, projection choice, stable ties, and
count and byte limits. Providers own domain matching and materialization. A
model-authored facet can describe why it is useful, but it cannot grant itself
injection or assign its own rank.

Advertising should be cheaper than materializing. Do not read a full document
or render an expensive view merely to discover that the discriminator will
drop it. Estimates are admission hints, not permission to overflow: the
materialized value must still fit whole and should be dropped rather than
silently clipped when it does not.

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
- Providers that inject eagerly when they can advertise a bounded projection
- Model-authored relevance scores treated as shared ranking authority
- A "Guidelines" dumping ground where a hazard, a toolkit fact, and a
  style note share equal weight, far from the steps they govern

## Placement And Ownership

- `internal/runtime/agent` owns assembly, section ordering, and capability-gated
  injection
- domain packages own projection from raw domain state into model-facing
  views
- `internal/state/awareness` owns shared time and recency helpers plus other
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
3. Are tense, audience, and referents clear from the model's point of view?
4. Does this belong in stable core context, tagged guidance, continuity
   context, related context, live state, a tool result, or nowhere at all?
5. If this data changes often, why is it static?

If those questions are answered well, the formatting is probably on the
right track.

## Current Reference Points

Good places to look for existing patterns:

- `internal/model/promptfmt/timefmt.go`
- `internal/state/awareness/entity_format.go`
- `loops/ego.md` — an instruction document shaped by the job each form
  does (prose stance, numbered protocol, constraints at their point of
  application)
- `internal/runtime/agent/tag_context.go`
- `internal/integrations/forge/context.go`
- `internal/channels/notifications/history_provider.go`
- `internal/runtime/agent/channel_provider.go`

## Related

- [`prompt-caching.md`](prompt-caching.md) — how system prompt sections
  map onto global stability classes, provider-specific cache policy,
  and the decision tree for adding a new section without breaking the
  cache.
