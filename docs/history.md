# Project History

Thane's first commit landed on February 5, 2026. Two months and 1,500
commits later, it had grown from a weekend experiment into something with
opinions about how AI should live in a home. This is the story of how
that happened.

## The Beginning (February 5-10)

The initial scaffolding went up in a single day: a Go binary, an event
loop, a Home Assistant REST client, and an Ollama integration. By the
end of day one, Thane could answer questions about HA entities. By day
five, it had SQLite-backed conversation memory, compaction, streaming
responses, a talent system for behavioral guidance, intelligent model
routing with an audit trail, and a task scheduler. The first release,
v0.2.0, tagged on February 10, was already a functional conversation
agent.

The velocity wasn't accidental. The project started with a clear thesis:
Home Assistant's built-in Assist had hit a ceiling, and the gap between
"voice assistant that controls pre-selected entities" and "autonomous
agent that understands your home" was an architecture problem, not a
model problem. The right LLM plumbing, connected to the right data,
would produce something qualitatively different.

## The Archive Era: v0.3.0 (February 11)

v0.3.0 was about memory depth. The session archive gave Thane immutable
conversation transcripts with FTS5 full-text search — you could ask
"what did we discuss about the garage last week?" and get real answers.
An OpenClaw session importer brought in conversation history from an
earlier experiment, giving Thane memories it hadn't lived through.
Session metadata generation (titles, tags, summaries) made the archive
navigable. AGENTS.md appeared here too — the first gesture toward making
the repo legible to AI visitors.

## Observability and Polish: v0.4.0 (February 12)

A short release focused on operational visibility. Comprehensive agent
loop logging, FTS5 in production, compact runtime context with embedded
changelogs. The kind of work that makes the difference between a project
you can debug and one you can't.

## The Intelligence Layer: v0.5.0 (February 14)

Three things landed that changed Thane's character:

**Automatic fact extraction.** After each conversation, a classifier
evaluates whether new facts should be stored. Same-value observations
reinforce confidence; changed values trigger updates. Thane started
learning without being told to learn.

**Delegation.** The iter-0 tool gating architecture split the world into
orchestrator and delegate. The smart model plans; the cheap model
executes. This made frontier-quality reasoning affordable for daily use
and created the economic foundation everything else built on.

**Episodic memory and working memory.** The agent gained continuity
across sessions (episodic) and a scratchpad within sessions (working
memory). Combined with fact extraction, Thane now had three layers of
memory operating at different timescales.

Connection resilience (connwatch) also appeared here — exponential
backoff for HA and Ollama connections. Boring but essential. The kind of
thing that separates a demo from something you leave running.

## The Sensory Expansion: v0.6.0 (February 16)

Thane grew eyes and ears:

- **Home Assistant WebSocket** subscriptions for real-time state events
- **UniFi integration** for room-level presence via AP associations
- **Person tracking** with context injection
- **Web search** via SearXNG and Brave providers
- **Anticipation engine** that triggered the agent on matched state
  changes (later replaced by the autonomous loop system)

The anticipation engine was the first attempt at proactive behavior —
Thane watching for events and acting without being asked. The
implementation was eventually scrapped, but the intent survived and
evolved into something better.

## Communication: v0.7.0 (February 20)

Thane found its voice beyond the API:

- **Native email** with full IMAP/SMTP: read, search, compose, reply,
  move, with markdown-to-MIME conversion and auto-Bcc audit trail
- **Email polling** with high-water mark tracking so the agent wakes
  only on new messages
- **Signal messaging bridge** via signal-cli JSON-RPC — inbound and
  outbound encrypted messaging with typing indicators and reactions
- **Contact directory** with structured vCard-aligned storage
- **Session management** tools (close, checkpoint, split) giving the
  agent control over its own conversation lifecycle
- **Self-reflection** via periodic ego.md updates — the agent analyzing
  its own behavioral patterns

This was the release where Thane stopped being a chatbot and started
being an agent. It could receive a Signal message, reason about it,
check Home Assistant, compose an email, and remember the whole
interaction — all without a human in the loop.

## The Architecture Release: v0.8.0 (March 10)

v0.8.0 was the largest release by commit count (~200 commits) and
represented a fundamental architectural maturation:

**Trust zones.** Every contact got a trust classification (admin,
household, trusted, known) that gates email permissions, compute
allocation, notification priority, and proactive behavior. Enforcement
in Go, not prompts.

**CardDAV server.** Native contact sync with macOS, iOS, and
Thunderbird via RFC 6352. Contacts managed in Thane accessible from
your phone.

**Actionable notifications** with human-in-the-loop callbacks. The
agent can ask for approval, wait for a response, and act on the
decision.

**Capability tags.** Tools and talents organized by semantic domain,
activated on demand. Sessions start lean; the agent loads capabilities
as needed. This created delegation pressure by architecture — the
orchestrator reaches for delegation because it literally doesn't have
the tools to do the work directly.

**MCP hosting.** External tool servers bridged into the agent loop via
the Model Context Protocol. ha-mcp alone added 90+ HA tools.

**GitHub/Forgejo integration.** 18 native forge tools for issues, PRs,
code review, and search.

**Media analysis.** RSS/Atom feed monitoring, YouTube transcript
extraction, Obsidian-compatible analysis output.

**The loop system.** The anticipation engine was replaced by a universal
loop primitive — a single abstraction for request/reply conversations,
background delegation tasks, and autonomous loops. The metacognitive
reflection, ego maintenance, email polling, media feed checking, and
Signal bridge all migrated to loop infrastructure. A registry tracks
everything running.

**Vision.** Content-addressed attachment storage with image analysis
capabilities via Signal integration.

**MQTT telemetry.** Thane publishes its own operational state as
HA-discoverable entities — uptime, token consumption, model info.

The v0.8.x patch releases (v0.8.1 through v0.8.4) addressed the
inevitable edge cases: metacognitive state split-brain resolution,
IMAP literal consumption, multipart MIME parsing, and the kind of
real-world bugs that only surface in production.

## The Convergence: v0.8.4 to v0.9.0 (March-April)

After v0.8.4, the project entered an intensive consolidation phase.
517 commits landed between v0.8.4 and the v0.9.0 documentation freeze,
focused on architectural convergence rather than new features:

**Dynamic model registry.** The static config-driven model list gave
way to a live registry that merges configured models with runtime
inventory from providers. LM Studio joined Ollama and Anthropic as a
first-class local runner.

**Prompt and tool pruning.** The always-on system prompt shrank as
domain-specific doctrine moved behind capability tags. Entry-point
talents route knowledge loading by semantic domain. The agent's context
window stays lean until it needs depth.

**Loop convergence.** The loops-ng work unified all background
execution under a single Spec/Registry/Launch abstraction. Loop
definitions became declarative, persistable, and introspectable from
the dashboard.

**Dashboard evolution.** Force-directed graph visualization of all
running loops, real-time request windows, model registry browser,
capability catalog views. Operational visibility went from "check the
logs" to "glance at the dashboard."

**Autonomous loops.** The most philosophically significant development.
Persistent loops that control their own pacing — the agent decides when
to look, how long to dwell, and when to move on. Jitter ensures no two
cycles are identical. Self-paced and event-driven flavors. This is
where Thane's agency lives: the ability to wonder about something and
then actively look for it.

**Anthropic prompt caching.** Native integration with Anthropic's cache
control for system prompt efficiency.

## The Documentation Overhaul (April 6)

The v0.9.0 documentation overhaul restructured everything:
flat `docs/` became `understanding/`, `operating/`, `reference/`.
AGENTS.md was rewritten as the front door for AI visitors. Issue and
PR templates, CODEOWNERS, and a dependabot configuration brought
process structure. GoDoc compliance across all packages. godoclint
and lychee link-checking added to the CI gate.

## Versioning

Thane's version numbers reflect momentum, not ceremony. The jump from
v0.2.0 to v0.8.0 happened in 33 days. Multiple minor versions tagged
on the same day. Patch releases when production found an edge case.

This is intentional. Early in a project's life, version numbers should
communicate velocity and progress, not API stability guarantees. Every
release is a snapshot of a working system that someone is running in
production. The version number tells you roughly how much has changed
since you last looked.

v0.9.0 is the first release that represents a deliberate pause for
documentation, process, and architectural consolidation rather than
feature velocity. It's the release where the project decided it was
mature enough to explain itself clearly.

## By the Numbers

As of v0.9.0:

- **1,500+ commits** across 60 days of active development
- **688 pull requests** (92% merged)
- **668 issues** (92% closed)
- **48 internal packages** in the Go codebase
- **80+ native tools** organized by capability tags
- Runs in production monitoring **15,000+ Home Assistant entities**
- Process memory: **~114 MB** on an M1 MacBook Air

## Who Built This

Thane is built collaboratively by David McNett and a rotating cast of
AI coding agents — Claude Code, OpenAI Codex, GitHub Copilot, and
Thane itself (via the `thane-developer` and `thane-agent` commit
identities). The git log is a genuine record of human-AI pair
programming: architecture decisions made in conversation, code written
by agents, reviewed by humans and other agents, refined through
iteration.

The project's name comes from a Scottish term for a landholder who
managed an estate on behalf of the crown. The AI agent that manages
your home automation estate. The name was chosen on day one and never
reconsidered.
