# Architecture

This document explains *why* Thane is built the way it is — the forces
that shaped the design and the trade-offs behind each structural choice.
For the project's values and goals, see [Philosophy](philosophy.md). For
how to operate it, see [Operating](../operating/).

## One Loop to Run Them All

Every piece of work in Thane — a user conversation, a delegated task, a
background watcher — runs as a loop. Not different subsystems with
different execution models, but the same primitive: context assembly,
tag activation, planning, tool execution, response.

This wasn't the original design. Early versions had separate code paths
for conversations, scheduled tasks, and the metacognitive reflection
process. Each had its own lifecycle management, its own error handling,
its own shutdown sequence. When we added email polling, it was another
special case. Signal bridge, another. Every new event source meant
another bespoke execution path to maintain.

The loop registry replaced all of that with a single abstraction.
Request/reply for conversations. Background tasks for delegation.
Autonomous loops for persistent observers. One registry tracks them
all, one shutdown path drains them all, one dashboard shows them all.
New event sources plug into an existing primitive instead of inventing
a new one.

The autonomous loop variant is where this pays off most. The agent
controls its own pacing via `set_next_sleep` — deciding after each
iteration how long until it looks again. Jitter breaks periodicity.
The model doesn't run on a cron schedule; it directs its own attention.
See [The Agent Loop](agent-loop.md).

## Capability Tags: Delegation Pressure by Architecture

An early design question: how does the orchestrator model decide when
to delegate versus when to act directly? The naive answer is prompt
instructions ("delegate when the task is tool-heavy"). That works
sometimes. Models ignore it other times.

Capability tags solve this structurally. Tools and talents are organized
by semantic domain (`ha`, `email`, `forge`, `web`). Sessions start with
only the `always_active` tags — core tools like memory, notifications,
session management. Maybe 15-20 tools visible.

When a request needs Home Assistant control, the orchestrator has two
choices: activate the `ha` tag and do it directly, or delegate to a
local model that already has those tools. The delegation tool is always
available. The HA tools are not — unless the orchestrator explicitly
asks for them.

This creates delegation pressure by architecture, not instruction. The
model delegates because it literally doesn't have the tools to do the
work any other way. No prompt compliance required.

Tags also keep context lean. A conversation about email doesn't load
HA tools, forge tools, or media tools. The system prompt stays focused
on what matters for the current task. See [The Agent Loop](agent-loop.md).

## Delegation: Separate Thinking from Doing

Frontier models are expensive per token. Tool-heavy tasks — searching
entities, calling services, verifying state — can burn through dozens
of iterations. Each iteration re-sends the full conversation context.

Delegation splits the work: the orchestrator plans the approach and
writes precise instructions. A delegate executes the tool-heavy work
and returns a summary. The orchestrator never sees the tool calls or
intermediate state — it gets a result.

Sometimes the delegate is a small, fast local model because the task
is mechanical. Sometimes it's a large, slow local model because the
task is complex and you want the best result zero API tokens can buy.
The orchestrator chooses: delegate synchronously and wait for the
result, or delegate asynchronously and continue reasoning while the
work happens in the background. The point isn't that delegates are
cheap; it's that thinking and doing have different resource profiles
and shouldn't be forced through the same model on the same context
window.

Talent files bridge the knowledge gap. The orchestrator doesn't see
delegate tool schemas directly (they're gated by tags), but
`delegate-hints.md` teaches it what tools exist, what patterns work,
and what anti-patterns to avoid. The frontier model writes precise
delegation prompts without ever seeing the tool definitions.
See [Delegation](delegation.md).

## Memory: Honest Assessment

Memory in Thane is foundational in intent but cobbled in implementation.
The agent has real, working memory — it learns facts, remembers
conversations, searches its own history — and this genuinely changes
the experience compared to stateless chat. But the current
infrastructure is several separate stores that grew organically rather
than a cohesive system.

What exists today: conversation memory (active context with LLM-driven
compaction), semantic facts (learned knowledge with optional
embeddings), session archives (immutable transcripts with FTS5 search),
working memory (per-session scratchpad), and episodic summaries
(post-session fact extraction). Each works. They don't compose as
cleanly as they should.

The deeper problem is that Thane has multiple durable document stores —
persona files, talents, knowledge bases, generated reports, scratchpads
— that are currently treated as separate systems but are better
understood as **managed document roots with different integrity
policies**. What differs between them isn't their storage nature; it's
their authoring policy, indexing behavior, and provenance requirements.

The root-policy model lets each document store declare its integrity
tier: whether the model may author directly, whether writes go through
git-backed commit flows, whether signatures are expected, and whether a
root participates in indexing. Persona files can move toward
high-integrity roots with signed commits. Knowledge bases can use
managed authoring with optional git backing. Scratchpads can stay
low-integrity and even opt out of indexing. Same architectural
primitive, different policy dials.

The current implementation establishes policy-aware managed roots and
signed git-backed writes. Stricter load-time enforcement, such as
blocking activation of unsigned high-integrity content, builds on that
foundation rather than creating another storage lane.

## Trust Zones: Safety in Go, Not Prompts

Prompt instructions are behavioral controls. They reduce harmful
behavior but don't eliminate it — research shows models acknowledge
constraints and proceed anyway more than a third of the time.

Every safety-critical decision in Thane is enforced in Go code. Trust
zones are the primary mechanism: every contact has a classification
(`admin`, `household`, `trusted`, `known`) stored in the database and
validated by Go. The model cannot invent new zones or escalate trust
through conversation.

Trust zones gate everything: who gets frontier models versus local
triage, who the agent can email freely versus who requires confirmation,
notification priority, rate limits, proactive behavior thresholds. One
classification, enforced across every subsystem.

The orchestrator's tool set is itself a structural control. It
receives only ~15 tools. It cannot send email, control HA devices, or
write to forge — those capabilities require delegation or explicit tag
activation. The tools are not in the API call. The model cannot choose
to use a tool it doesn't have.

See [Trust Architecture](trust-architecture.md).

## Context Assembly: Identity, Knowledge, Awareness, Behavior

The system prompt is assembled from four layers, each with a distinct
purpose. Mixing concerns across layers degrades agent behavior — this
was learned empirically, not theorized.

**Persona** is identity: who the agent is, its voice and values.
**Inject files** are knowledge: factual reference material the agent
needs. **Current conditions** are awareness: time, environment, active
state. **Talents** are behavior: how the agent should act in specific
situations.

The assembly order mirrors natural orientation: I know who I am, I know
what I know, I know where and when I am, I know how to behave. Putting
tool rules in the persona suppresses personality. Putting identity in
talents creates contradictions. The layer separation isn't arbitrary —
each anti-pattern was discovered by watching the agent behave badly
when concerns were mixed. See [Context Layers](context-layers.md).

## Model Routing: Intent, Not Names

The router doesn't select models by name. It scores every configured
model on quality, speed, and cost, then matches the best model to the
request's routing hints: quality floor, speed preference, local-only
restriction, mission type.

This means the same codebase works whether you have one local model or
a fleet of cloud providers. Routing profiles (`thane:latest`,
`thane:premium`, `thane:ops`) describe intent — "I want the best model"
or "I want the cheapest model" — and the router resolves that intent
against whatever models are actually available.

These routing profiles are exposed as **virtual models** on the
Ollama-compatible API. When Home Assistant (or any Ollama client)
lists available models, it sees `thane:latest`, `thane:premium`,
`thane:command`, `thane:trigger`, and so on — each one a different
intent profile backed by the same agent. An operator can set up
multiple HA conversation agents pointing at different virtual models:
one for daily use, one for quick device control, one for automations
that need the cheapest path. The HA UI presents them as model choices;
Thane resolves each to the right real model at request time.

Routing hints propagate through delegation. When the orchestrator
delegates a task, the delegate inherits routing context. A quality
floor set on the original request carries through to the local model
selection. See [Routing Profiles](../operating/routing-profiles.md).

## Speaking Existing Protocols

Thane doesn't invent protocols. It speaks the ones the ecosystem
already uses:

The **Ollama-compatible API** on port 11434 means Home Assistant's
native Ollama integration connects without modification. From HA's
perspective, Thane *is* an Ollama instance. No custom integration, no
HACS, no protocol adapters.

The **OpenAI-compatible API** on port 8080 means any client that speaks
the chat completions API works out of the box. Open WebUI, custom
scripts, other agents.

**MCP** (Model Context Protocol) means external tool servers bridge
into the agent loop without writing Go code. ha-mcp alone adds 90+
Home Assistant tools.

**CardDAV** means contacts sync natively with macOS, iOS, and
Thunderbird. No export/import workflow, no proprietary sync.

**MQTT** means Thane publishes telemetry as HA-discoverable entities
using the protocol HA already speaks. Thane shows up as a device in
the HA UI automatically.

The result is a system that integrates deeply with existing
infrastructure without requiring that infrastructure to know Thane
exists.

## Technology Choices

**Go** because a single binary with no runtime dependencies is the
difference between something people run and something they try once.
No Python environments, no Docker requirements, no version conflicts.
Cross-compilation to any platform. Excellent concurrency for managing
multiple loops, connections, and event sources simultaneously.

**SQLite** because embedded databases eliminate an entire class of
deployment problems. No database server to manage, no connection
strings to configure, no schema migrations to coordinate. The database
files are just files — inspectable, backupable, portable. FTS5 gives
us full-text search without an external search engine.

**YAML** for configuration because it's human-readable and supports
environment variable expansion. Operators can read and understand
their config. The generated example config
(`examples/config.example.yaml`) is derived from Go struct tags and
field comments, so it cannot drift from the code.

**Markdown** for talents and inject files because natural language
carries nuance that structured configuration cannot. "Be more careful
about assumptions when the user corrects you" is a behavioral
directive that doesn't map to a boolean flag. Talents are transparent,
editable, version-controllable, and forkable.

## License

Apache 2.0 — aligned with Home Assistant.
