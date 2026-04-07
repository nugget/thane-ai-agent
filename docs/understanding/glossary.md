# Glossary

Canonical definitions for Thane-specific terminology. When documenting or
discussing Thane, use these terms consistently.

---

### Agent Loop

The universal execution primitive. Every piece of work — conversations,
delegations, background watchers — runs as a loop with the same reasoning
cycle: context assembly, tag activation, planning, tool execution, response.
Three operation modes: *request/reply* (one-shot, up to ten iterations),
*background task* (detached, result delivered later), and *autonomous loop*
(persistent, self-paced — the agent directs its own attention and controls
its own timing). A registry tracks all active loops.
See [The Agent Loop](agent-loop.md).

### Capability Tag

A semantic label (`ha`, `email`, `forge`, `web`, etc.) that gates which tools
and talents are active in a given session. Sessions start with a minimal set of
`always_active` tags. Additional tags are activated on demand — by the agent,
the user, or the channel. Tags create *delegation pressure*: the orchestrator
starts with a small toolset and naturally reaches for delegation when it needs
capabilities outside its active set.
See [The Agent Loop](agent-loop.md).

### CardDAV

The protocol Thane uses to sync its contact directory with native apps (macOS
Contacts, iOS, Thunderbird). Served on port 8843 via RFC 6352. Contacts
managed in Thane are accessible from your phone without export/import.
See [API & Endpoints](../reference/api.md).

### Carry-Forward

A context note written by the agent when closing a session. It summarizes
key decisions, open threads, and relevant state so the next session can
pick up where the previous one left off. The primary mechanism for
continuity across session boundaries.

### Channel

A communication pathway into or out of Thane: the web API, Home Assistant's
Ollama integration, email (IMAP/SMTP), Signal messaging, or MQTT. Each
channel can pin capability tags — an email-originated request automatically
activates the `email` tag.

### Checkpoint

A full state snapshot of conversations and metadata, persisted to SQLite.
Triggered by message count threshold, graceful shutdown, or manual request.
Checkpoints enable crash recovery — the agent resumes exactly where it left
off.
See [Memory](memory.md).

### Compaction

The process of summarizing older conversation messages to reduce token count
while preserving semantic content. Compaction happens automatically when
a conversation approaches context limits. The LLM generates the summary,
preserving decisions, facts, and preferences.

### Context Layer

One of four distinct sections of the system prompt, each with a specific
purpose: *Persona* (identity), *Talents* (behavior), *Inject Files*
(knowledge), and *Session Context* (current state). Mixing concerns across
layers degrades agent behavior.
See [Context Layers](context-layers.md).

### Delegate

A local model that executes tool-heavy work on behalf of the orchestrator.
Delegates receive precise instructions and a full toolset, execute
mechanically, and return structured results. They run at zero API cost.
See [Delegation](delegation.md).

### Delegation

The two-model pattern at the heart of Thane's cost architecture. The
orchestrator (cloud or large local) plans and reasons. The delegate (small
local) executes tool calls. The smart model thinks; the cheap model does.
See [Delegation](delegation.md).

### Egress Gate *(planned)*

A single enforcement point for all outbound messages (email, eventually
Signal and other channels). Will provide rate limiting by trust zone,
dedup, and content scanning. The most critical planned structural safety
control.
See [Trust Architecture](trust-architecture.md).

### Episodic Summary

A post-session analysis that extracts key facts from conversations into
the semantic knowledge base. Enables long-term learning and pattern
recognition across sessions.

### Event Source

Anything that can wake the agent loop: an API request, a Home Assistant
WebSocket state change, an MQTT wake subscription, an inbound email or
Signal message, or a scheduled task firing.
See [Event Sources](../reference/event-sources.md).

### Inject File

A reference document loaded into the system prompt's knowledge layer.
Contains factual information — user profiles, infrastructure notes,
identity documents — not behavioral directives.
See [Context Layers](context-layers.md).

### Lens

A persistent global behavioral mode that modifies context across all
conversations. Unlike capability tags (which activate tools), lenses
shape *how* the agent behaves. Activated and deactivated via tools.

### MCP (Model Context Protocol)

A standard protocol for extending LLM capabilities via external tool
servers. Thane hosts MCP servers as subprocesses and bridges their tools
into the agent loop. The primary example is ha-mcp, which provides 90+
Home Assistant tools.
See [Delegation](delegation.md).

### Metacognitive Loop

A scheduled self-reflection process where the agent analyzes recent
interactions and updates its personality file (`ego.md`). Runs on a
quality-floor model. Constrained to observation — reads files and
memory but doesn't execute tools or interact with external systems.

### Orchestrator

The primary model in a delegation interaction. Plans the approach,
writes precise instructions for the delegate, and shapes the final
response from the delegate's execution results. Typically a cloud or
large local model.
See [Delegation](delegation.md).

### OpState

A generic namespaced key-value store with TTL support. Persists
lightweight operational state: email polling cursors, feature flags,
session preferences. Not for structured domain data.

### Persona

A markdown file (`persona.md`) that defines the agent's identity —
voice, personality, values, boundaries. Loaded into the system prompt's
identity layer. The persona shapes how the agent interprets ambiguous
requests and communicates.
See [Context Layers](context-layers.md).

### Routing Hint

A parameter that influences model selection for a specific request.
Hints include quality floor, speed preference, local-only restriction,
and mission type. Hints propagate through delegation.
See [Routing Profiles](../operating/routing-profiles.md).

### Routing Profile

A named preset that maps to a set of routing hints. Selected by setting
the model name in any Ollama-compatible client (e.g., `thane:latest`,
`thane:premium`, `thane:ops`). Profiles describe intent; the router
finds the best model.
See [Routing Profiles](../operating/routing-profiles.md).

### Semantic Fact

A piece of learned knowledge stored with optional embeddings for natural
language recall. Categorized (user, home, device, routine, preference)
and confidence-scored. Facts are long-term memory — they persist across
sessions, restarts, and model changes.
See [Memory](memory.md).

### Session

A bounded conversation with its own message history, working memory,
and metadata. Sessions can be closed (with carry-forward), checkpointed
(for recovery), or split (forking context). Completed sessions are
archived with full-text search.

### Talent

A markdown file that teaches the agent a specific behavior or skill.
Talents are tag-filtered — each declares which capability tags it
requires and only loads when those tags are active. Transparent,
editable, version-controlled. Natural language as the configuration
mechanism.
See [Context Layers](context-layers.md).

### Trust Zone

A classification assigned to every contact: `admin`, `household`,
`trusted`, `known`, or `unknown` (implicit). Trust zones are the
universal router for permissions across the system — gating email send,
compute allocation, notification priority, and proactive behavior.
Validated in Go, not prompts.
See [Trust Architecture](trust-architecture.md).

### Working Memory

A per-session read/write scratchpad for transient context — emotional
texture, conversational arc, things worth tracking within a conversation
but not permanently. Survives compaction within a session but doesn't
persist across sessions.
See [Memory](memory.md).
