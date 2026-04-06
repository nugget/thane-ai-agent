# Prompt And Tool Pruning Strategy

This document tracks the ongoing effort to shrink Thane's always-on
system prompt, reduce tool-surface doctrine in immortal context, and
move most operational guidance behind capability-scoped tagged context.

It is a strategy and tracking document, not a frozen design. The exact
runtime contract, tagged KB article shapes, and capability-loading
posture will continue to evolve as we test behavior on real loops.

## Why This Exists

Recent live prompt inspection showed a familiar failure mode:

- too much always-on tool exposition
- duplicated capability and tool guidance across multiple prompt layers
- too much encouragement to activate capabilities speculatively
- not enough crisp instruction about exact visible tools and gated
  top-level execution

In practice, this encourages models to browse the capability universe
instead of solving the current turn with the tools already in hand. It
also increases prompt size, weakens signal density, and makes regressions
harder to reason about because too many layers are trying to teach the
same thing.

## Current Runtime Shape

Today, prompt assembly in `internal/agent/loop.go` roughly looks like:

1. persona
2. ego
3. runtime contract
4. injected core context files
5. tagged capability context
6. active capability summary
7. model-family tool-calling contract
8. current conditions
9. talents
10. dynamic context
11. conversation history and carry-forward context

This layering is mostly sound. The problem is that the always-on layers
still carry too much evergreen tool doctrine, and the tagged/contextual
layers are not yet doing enough of the behavioral teaching.

## Fresh Production Snapshot

On April 6, 2026 we inspected a fresh retained production prompt from
`r_aac0d80a025e3a61.json`. The total prompt length was 110,466
characters.

The high-level sections measured roughly:

- persona: 4.1k
- ego: 9.8k
- runtime contract: 1.2k
- injected core context: 1.8k
- capability context: 17.9k
- active capabilities: 1.4k
- current conditions: 0.2k
- behavioral guidance: 25.3k
- relevant context: 6.5k
- conversation history: 42.5k

That snapshot matters for two reasons:

- the prompt is still far larger than it needs to be for routine turns
- tool and capability doctrine are duplicated across capability context
  and behavioral guidance, not just immortal prompt layers

### Concrete duplicated sections from the snapshot

The retained prompt still included all of these sections:

- `# Tool Reference & Pitfalls`
- `# Writing Quality GitHub Issues`
- `# Thane Development Workflow`
- `# How the Knowledge Base Works`
- `# Tool Hygiene`
- `# Delegation`
- `# Knowledge`
- `### Available Capabilities`

This is the exact duplication pattern we want to remove.

### Important nuance

The fresh snapshot showed that the old tool essays are not only in
always-on talents. A large portion of them are arriving through tagged
capability or channel context as well. That means the pruning pass
cannot be limited to a single prompt file. We need to trim both:

- the base or talent-side teaching
- the tagged KB or channel-side articles that are currently overstuffed

## Problems To Fix

### 1. Always-on prompt is too large

The immortal prompt should teach stable identity and execution
semantics, not carry a whole encyclopedia of every tool domain.

### 2. Tool guidance is duplicated

The same ideas currently appear in several places:

- runtime contract prose
- active capability descriptions
- talents
- injected core documents
- tagged capability context

This duplication makes it hard to know which layer actually taught a
behavior, and it causes drift when one layer is updated but another is
not.

### 3. Capability activation posture is too loose

The model currently receives too many signals that make activation feel
cheap and broadly desirable. That encourages rapid-fire ungating instead
of:

- using already loaded tools first
- activating one missing domain deliberately
- delegating when a task obviously spans multiple domains

### 4. Tagged context is underused

We now have the right substrate for targeted context:

- helper tags like `interactive`, `owu`, `signal`, and `owner`
- protected tags like `owner`
- per-channel tag activation
- KB article injection by tag

We should lean much harder on that system and remove equivalent
always-on prose.

## Target Shape

### Always-on prompt should stay small

Always-on context should mostly contain:

- persona and identity
- ego / self-reflection
- a short runtime contract
- fixed core documents from `{workspace}/core`
- current conditions
- active loaded capabilities only
- the model-family tool-calling contract
- dynamic context for the current turn

### Tagged context should carry domain doctrine

Domain-specific behavioral guidance should move behind tags, for example:

- `forge` for GitHub/forge workflows
- `ha` for Home Assistant control norms and caution
- `search` for search and fetch habits
- `interactive` for owner-facing conversational loops
- `signal` for Signal-specific tone and response patterns
- `owu` for Open WebUI specifics
- `owner` for privileged owner-facing guidance

### Tool schemas should carry exact names and parameters

The prose prompt should not try to restate the whole tool catalog.

The prompt should teach:

- use exact visible tool names
- do not guess unavailable tools
- activation changes runtime state but does not guarantee top-level
  access to every tool
- delegate when the needed tool is gated out of the current loop

The schemas, manifests, and loaded-capability summary should carry the
rest.

## Capability Activation Strategy

The desired model posture is:

- use loaded tools first
- activate narrowly when blocked
- activate one capability at a time
- after activation, reassess before activating anything else
- if the task clearly spans several domains, prefer `thane_delegate`
  over serial capability churn

This is the opposite of a "browse and load everything" posture.

## Layer Responsibilities

### `workspace/core`

Core files are the canonical always-on documents rooted at
`{workspace}/core`. They should stay small, high-signal, and stable in
purpose:

- `persona.md`: identity, voice, values
- `ego.md`: self-reflection and continuity of internal stance
- `mission.md`: durable mission framing and major operational truths
- `metacognitive.md`: metacognitive loop state, not general doctrine

These files should not become a dumping ground for tool encyclopedias.

### Talents

Talents should teach portable behavioral patterns, not enormous static
inventories of current runtime capabilities. When a talent starts to
look like a domain-specific operator manual, it is a candidate for
tagged KB context instead.

### Tagged KB Articles

Tagged KB articles are the preferred home for:

- domain-specific tool norms
- channel-specific guidance
- trust-zone-specific operator guidance
- examples that are useful only when a related capability is active

## Near-Term Pruning Plan

- [ ] Audit the exact always-on sections currently emitted by
      `buildSystemPromptWithProfile`.
- [ ] Record section-size measurements from real retained prompts and
      keep this tracking doc updated with current evidence.
- [ ] Remove duplicated tool/capability essays from always-on prompt
      assembly.
- [ ] Keep only a compact runtime contract in `internal/prompts/agent.go`.
- [ ] Shrink `## Active Capabilities` to loaded-state facts, not
      capability doctrine.
- [ ] Move forge, search, Home Assistant, and channel-specific teaching
      into tagged KB articles.
- [ ] Add targeted tagged articles for `interactive`, `signal`, `owu`,
      and `owner`.
- [ ] Audit `{workspace}/core` files so they reflect identity and mission
      rather than operational sprawl.
- [ ] Revisit talents and delete any sections that are now better served
      by tagged articles.
- [ ] Capture literal retained prompt snapshots before and after pruning
      so regressions are observable.

## First Safe Cuts

Based on the April 6 production snapshot, the first low-risk cuts
should be:

1. Keep `## Runtime Contract`, but make it the only always-on tool
   doctrine block.
2. Remove `### Available Capabilities` from behavioral guidance.
   Loaded capability state already appears in `## Active Capabilities`.
3. Remove `# Tool Hygiene`, `# Delegation`, and `# Knowledge` from
   talents or other always-on behavioral guidance.
4. Split the giant tagged or channel article currently carrying:
   - `# Tool Reference & Pitfalls`
   - `# Writing Quality GitHub Issues`
   - `# Thane Development Workflow`
   - `# How the Knowledge Base Works`
5. Re-home those into narrower tagged articles such as `forge`,
   `interactive`, `owner`, or a future repo or development tag.
6. Keep channel-specific voice and tone sections with the channel
   article, but move tool and operator manuals out of it.

This should materially reduce prompt size before we touch more
sensitive areas like conversation history or ego shaping.

## Guardrails

- Do not solve prompt bloat by creating a different giant prompt in a
  new file.
- Do not make the runtime contract a dumping ground for domain-specific
  examples.
- Do not hide live runtime truth in static markdown when Go can inject
  it dynamically.
- Do not encourage speculative capability activation.
- Do not duplicate the same instruction in core files, talents, and
  tagged articles unless there is a clear reason for repetition.

## Open Questions

### How much should active capability state teach?

The loaded-capability summary likely still needs to tell the model what
is already active and what is safe to do with that state. It probably
should not try to be a full tutorial.

### How much tool doctrine belongs in talents vs tagged KB?

The current bias should be:

- talents for durable behavioral style
- tagged KB for domain/operator doctrine

But the exact split will need iteration.

### Should different model families get different prompt contracts?

Probably yes, but through a shared model-family adapter layer rather
than several hand-maintained prompt forks.

## Success Criteria

We should consider this work successful when:

- the always-on prompt is materially smaller
- tool-calling errors from guessed or gated tool names decrease
- capability activation becomes more deliberate
- tagged context explains domain-specific behavior better than the old
  always-on prose
- prompt debugging becomes easier because each layer has a clearer job
