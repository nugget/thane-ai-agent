# Prompt And Tool Pruning Strategy

This document tracks the ongoing effort to shrink Thane's always-on
system prompt, reduce tool-surface doctrine in immortal context, and
move most operational guidance behind capability-scoped tagged context.

It is a strategy and tracking document, not a frozen design. The exact
runtime contract, tagged KB article shapes, and capability-loading
posture will continue to evolve as we test behavior on real loops.

The writing standard is not "dry" or "purely procedural." Model-centric
phrasing, philosophy, and felt bias are all welcome when they create
better behavior. The rule is simply that each line must be doing real
work.

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
- `web` for remote retrieval and wider-web discovery habits
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

## Progressive Disclosure Model

The current working model is a coarse-to-fine capability menu backed by
entry-point documents that lead into a broader decision tree.

The important design choice is that these should be the same documents,
not two parallel systems:

- the manifest exposes a small set of broad entry-point tags
- activating one broad tag loads a tagged article for that domain
- that article acts like a local decision tree
- the article defines the next tier of options, narrower tags, and
  delegation boundaries

In other words, the "phone tree" is only the navigation posture. The
actual content should live in normal tagged KB articles that remain
useful even outside that posture.

### Initial root menu tags

The first menu-sized entry points should stay small:

- `interactive`
- `development`
- `home`
- `people`
- `knowledge`
- `operations`
- `media`
- `owner` (protected; trustworthy when present, not manually activated)

These tags are broad on purpose. Their job is to help the model choose
the next tag or act with what it already has, not to unlock the whole
world immediately.

Do not solve every missing family by adding another root. Prefer
second-hop specialization routes from an existing root when the domain
already has a natural home:

- `archive` under `knowledge`
- `session` under `interactive` and `operations`
- `email` under `people`
- `mqtt` under `operations`

### Entry-point document shape

Each root-tag entry-point document should answer only local questions:

- what kind of request belongs here
- what tools or narrower tags are usually relevant next
- when to stop narrowing and just use currently visible tools
- when to delegate instead of serially loading more context

The teaser already did the activation advertisement. Once the tag is
active, the entry-point body should not waste space repeating that same
pitch after the heading.

Every byte in an entry-point file should make the next choice easier.

That keeps the documents operator-friendly and avoids recreating the old
"one immortal encyclopedia" problem in KB form.

### Distinguishing entry points from articles

Entry points and doctrine articles should remain separate documents even
when they share a tag.

The current working convention is:

- `kind: entry_point` for the first navigation or triage document on a
  path
- filename suffix `-entry-point.md` for entry-point documents so they
  are easy to distinguish from doctrine without consuming a root-name
  prefix
- `teaser:` for short pre-activation menu copy
- `next_tags:` for structured likely follow-on tags
- no `kind` field for ordinary doctrine or knowledge articles

When both exist for the same active tag, entry points should appear
first so the model sees navigation scaffolding before richer doctrine.

The broader filename vocabulary should also stay explicit for authors,
even though the model never sees filenames directly:

- `foundation.md` for the always-injected behavioral substrate that
  re-establishes posture before any tagged guidance loads
- `*-doctrine.md` for operational guidance inside a domain
- `*-communication.md` for tone, pacing, rapport, and felt presence in
  a domain

These jobs are different. Do not collapse them casually. Navigation,
operation, and felt presence strengthen each other best when each stays
clear about its role.

For model-facing copy inside the document itself:

- heading format should be `# <Domain> Entry Point`
- use one stable cue line: `Choose the next move deliberately:`
- keep bullet verbs inside the fixed navigation vocabulary where
  possible (`activate`, `use`, `delegate with`, `read`, `respond`)

### Site-local overlays

Some capability families are real on one deployment but weak, absent, or
shaped differently elsewhere. `pocket` is the obvious current example:
its Home Assistant MCP bridge, MQTT wake plumbing, host-local ops
surface, and older local tag aliases are not universal truths for every
Thane runtime.

The working convention should be:

- keep global root entry points in the shared repo corpus
- keep exactly one `kind: entry_point` document per tag in a deployed
  talents directory
- if a site truly needs a different root path for a tag, edit that
  site's local `<tag>-entry-point.md` directly instead of adding a
  second competing entry point
- express site-specific guidance as tagged local doctrine, not as new
  global roots
- route site-specific capabilities from the nearest existing root rather
  than multiplying top-level menu tags

Recommended local filename pattern:

- `<tag>-<site>-local.md` for site-specific doctrine hanging off an
  existing root or branch tag

Examples for `pocket`:

- `home-pocket-local.md` for local home/planning/MCP branching
- `operations-pocket-local.md` for host ops, MQTT, and local operational
  branches
- `ha-pocket-local.md` for native HA vs bridged HA MCP guidance

The purpose of these files is not to create a second menu. Their job is
to make the nearest real menu node smarter on a specific site.

### Fixed tree-navigation verbs

Tree navigation should use a tiny stable verb set so the model is not
forced to interpret synonyms:

- `activate <tag>`: load an activatable capability tag
- `use <tool>`: call a tool that is already visible in this turn
- `delegate with <tags>`: hand off bounded work with explicit tags
- `read <reference>`: inspect a specific semantic path or file reference
- `respond`: answer directly without further tool churn

Protected tags should not be phrased as `activate <tag>`. Their
presence communicates a trustworthy fact about the current loop, so they
should be described as already present or not present.

### Knowledge path pilot

`knowledge` is the current pilot path because it has low channel
complexity and a relatively shallow branch structure.

The current working depth is:

- depth 0: menu entry `knowledge`
- depth 1 choices:
  - `activate files`
  - `activate memory`
  - `activate web`
  - `delegate with files,web`
  - `respond`
- depth 2 doctrine:
  - `files`: semantic references, document reads, quoting, editing
  - `memory`: compact durable facts vs documents
  - `web`: remote retrieval, outside verification, and discovery

That is intentionally shallow. A good early guardrail is to resist
going deeper than roughly two hops of branching until we have evidence
that the extra depth materially helps. This is also a likely future
model-family tuning knob: stronger models may tolerate a broader or
deeper tree than weaker ones.

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
- [ ] Move forge, web, Home Assistant, and channel-specific teaching
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

One concrete cut already landed on this branch: the always-on
`talents/delegation.md` file was reduced to universal delegation
doctrine, with the exhaustive failure tables and tool-specific gotchas
removed from immortal prompt space.

Another concrete cut now landed as well: `talents/communication.md` was
reduced to universal communication guidance, while interactive
conversation behavior and development-writing norms moved into tagged
talents for `interactive` and `development`/`forge`.

Another clean split followed: `talents/presence.md` now stays focused on
restraint, anticipation, and felt presence, while interruption and alert
thresholds moved behind tagged notification guidance.

The root-menu experiment is also live in the prompt path now, not just
in examples: broad tags like `development`, `home`, `interactive`,
`knowledge`, `media`, `operations`, and `people` now have tagged
entry-point talents, and tagged doctrine slices like `forge`,
`interactive`, and `notifications` now load as real behavioral guidance
when active.

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
