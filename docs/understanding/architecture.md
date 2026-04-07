# Architecture

> For the philosophy behind these decisions, see [Philosophy](philosophy.md).

## Design Principles

### Local-First, Cloud-Optional

Thane can run entirely on local model runners. Every conversation stays on
your hardware. Cloud models are available for complex reasoning, but
nothing requires them.

### Memory as Foundation

Memory isn't bolted on — it's foundational. Semantic facts with embeddings,
conversations that persist across sessions, checkpoints for crash recovery,
searchable archives, and episodic summaries for long-term learning. The
agent can ask "what do I know about the garage?" and get structured answers.
See [Memory](memory.md).

### Talent-Driven Behavior

Agent personality and behavioral guidance live in markdown files. This keeps
behavior transparent, editable, and version-controlled. Natural language is
the configuration mechanism — it carries nuance that structured config
cannot. Talents are tag-filtered: a talent tagged `[email]` only loads when
the `email` tag is active. See [Context Layers](context-layers.md).

### Contact Directory and Trust Zones

Every person the agent interacts with gets a persistent contact record with
trust zone classification (`admin`, `household`, `trusted`, `known`). Trust
zones are the universal router — they gate email permissions, compute
allocation, notification priority, and proactive behavior. Enforcement is
structural: validated in Go, not prompts.
See [Trust Architecture](trust-architecture.md).

### Dual-Port Architecture

Thane serves two APIs simultaneously:

- **Port 8080** — Native API (OpenAI-compatible `/v1/chat/completions`),
  web dashboard, and built-in chat interface
- **Port 11434** — Ollama-compatible API. Home Assistant's native Ollama
  integration connects here without modification

This means Thane works with HA out of the box — no custom integration needed.

## System Overview

```
┌───────────────────────────────────────────────────────────────────┐
│                          Thane Agent                               │
├───────────────────────────────────────────────────────────────────┤
│                                                                    │
│  ┌──────────────┐  ┌──────────────┐  ┌─────────────────────────┐  │
│  │  Native API  │  │ Ollama-Compat│  │     Event Sources       │  │
│  │  (port 8080) │  │ (port 11434) │  │  HA WS │ MQTT │ Email  │  │
│  │  + Dashboard │  │              │  │  Signal │ RSS Feeds     │  │
│  └──────┬───────┘  └──────┬───────┘  │  Scheduler              │  │
│         │                 │          └────────┬────────────────┘  │
│  ┌──────┴───────┐         │                   │                   │
│  │ CardDAV Srvr │         │                   │                   │
│  │ (port 8843)  │         │                   │                   │
│  └──────┬───────┘         │                   │                   │
│         └─────────────────┼───────────────────┘                   │
│                           ▼                                        │
│  ┌─────────────────────────────────────────────────────────────┐  │
│  │                      Agent Loop                              │  │
│  │                                                              │  │
│  │  Context Assembly → Tag Activation → Planning → Delegation   │  │
│  │        ↑                                  │                  │  │
│  │    Talents &                          ┌───┴────┐             │  │
│  │    Memory &                           ↓        ↓             │  │
│  │    Contacts                      Direct    Delegate          │  │
│  │                                (orchestr.) (local model)     │  │
│  └─────────────────────────────────────────────────────────────┘  │
│         │                │                    │                     │
│         ▼                ▼                    ▼                     │
│  ┌─────────────┐  ┌────────────┐  ┌───────────────────────────┐  │
│  │   Memory    │  │   Model    │  │      Integrations         │  │
│  │   Store     │  │   Router   │  │  HA (REST/WS) │ MCP Host  │  │
│  │  (SQLite)   │  │ (scoring)  │  │  Email │ Signal │ Forge   │  │
│  └─────────────┘  └────────────┘  │  Shell │ Files │ Search   │  │
│                                    └───────────────────────────┘  │
│  ┌─────────────┐  ┌────────────┐  ┌───────────────────────────┐  │
│  │  Scheduler  │  │    MQTT    │  │  Contacts & Trust Zones   │  │
│  │  (cron)     │  │ Publisher  │  │  Notifications (HITL)     │  │
│  └─────────────┘  └────────────┘  └───────────────────────────┘  │
│                                                                    │
│  ┌─────────────┐  ┌────────────┐  ┌───────────────────────────┐  │
│  │   OpState   │  │ Self-Refl. │  │    Event Bus              │  │
│  │  (KV store) │  │ (ego.md)   │  │  (pub/sub observability)  │  │
│  └─────────────┘  └────────────┘  └───────────────────────────┘  │
│                                                                    │
└───────────────────────────────────────────────────────────────────┘
```

## Components

### Agent Loop

The universal execution primitive. Every piece of work — a user
conversation, a background delegation, a persistent watcher — runs as
a loop with the same reasoning cycle: context assembly, tag activation,
planning, tool execution, response. Loops come in three modes:
request/reply (one-shot conversations), background tasks (detached
delegation), and autonomous loops (persistent, self-paced — the agent
directs its own attention). A registry tracks all active loops and
coordinates shutdown.
See [The Agent Loop](agent-loop.md).

### Model Router

Score-based routing that selects the right model for each task. Models are
scored on quality (1-10), speed (1-10), and cost tier (1-5). Routing hints
control selection: quality floor, speed preference, local-only restriction,
mission type. The router doesn't hardcode model names — it describes intent,
and the scoring finds the best match.
See [Routing Profiles](../operating/routing-profiles.md).

### Memory Store

SQLite-backed with optional vector search. Conversations, memory, and
scheduler state live in a unified `thane.db`. Five memory types: semantic
facts (long-term learned knowledge), conversation memory (short-term
context), working memory (per-session scratchpad), session archives
(searchable history), and episodic summaries (post-session consolidation).
See [Memory](memory.md).

### Home Assistant Integration

Deep integration via multiple protocols: REST API for state and service
calls, WebSocket for real-time event subscriptions, MCP for 90+ additional
tools via ha-mcp, and MQTT for publishing Thane's own telemetry as
HA-discoverable entities.
See [Home Assistant](../operating/homeassistant.md).

### Delegation System

The orchestrator model plans; local models execute. Delegation is the core
cost optimization — the frontier model uses ~62K tokens for orchestration,
the local model uses ~32K tokens for execution at zero cost. Delegate
profiles control routing, and talent files teach the orchestrator how to
write effective delegation prompts.
See [Delegation](delegation.md).

### Native Email

Full IMAP/SMTP support: read, search, compose, reply, move. Markdown-to-MIME
conversion for rich HTML email. Multi-account with lazy connections and health
monitoring. Trust zone gating on all outbound recipients. Auto-Bcc owner for
audit trail. Polling via scheduler with opstate high-water marks.

### Signal Messaging

Inbound and outbound messaging via signal-cli JSON-RPC bridge. Trust zone
gating and rate limiting. Threading support across Signal groups.

### Contact Directory

Persistent contacts with vCard-native storage. Structured properties (email,
phone, Signal handle) with preference ordering and last-interaction tracking.
CardDAV server (port 8843) for native sync with macOS/iOS/Thunderbird.
VCF import/export with QR code generation.

### Capability Tag System

Tools and talents organized by semantic tags. Sessions start minimal;
additional tags activate on demand. This creates delegation pressure by
architecture — the orchestrator starts with ~15-20 tools and naturally
reaches for delegation when it needs more.
See [The Agent Loop](agent-loop.md).

### Notification System

Provider-agnostic delivery with human-in-the-loop callback support.
Fire-and-forget notifications and actionable notifications with response
buttons. Configurable timeouts with auto-execute, escalation, or cancel
behaviors. Contact-aware routing. MQTT-based callback dispatch.

### Task Scheduler

Cron-style scheduling with per-task model and routing overrides. Missed
execution recovery. Persistent in SQLite.

### Self-Reflection

Periodic analysis of recent interactions that updates `ego.md` — a
personality file tracking behavioral patterns and growth areas. Runs on a
quality-floor=7 model. Constrained to observation: reads files and memory
but doesn't execute tools or interact with external systems.

### Web Dashboard

Built-in operational interface at port 8080: overview with health status,
interactive chat, contact directory browser, semantic fact store, session
archive with transcripts, and task scheduler. Powered by htmx.

### Event Bus

In-process publish/subscribe for operational observability. Source-tagged
events from the agent loop, Signal bridge, delegate, email, metacognitive
loop, and scheduler. Powers the dashboard's real-time execution view.

### Structured Logging

Three-layer infrastructure: self-managed file rotation with gzip
compression, context-propagated session-scoped loggers with subsystem tags,
and a SQLite index for fast queries by session, level, subsystem, and time
range. Background pruner manages retention.

### MQTT

Thane publishes operational telemetry as HA-discoverable entities: uptime,
daily token consumption, default model, last request timestamp, and version.
Subscribes to configurable topics for wake-on-MQTT event triggers.
See [MQTT](../operating/mqtt.md).

### Operational State Store

Generic namespaced KV store with TTL support. Persists email polling cursors,
feature flags, and session preferences. Not for structured domain data —
those get their own stores.

### Media Feeds

RSS/Atom feed monitoring with periodic polling and new-entry detection.
Transcript extraction from YouTube and podcasts via yt-dlp. Structured
analysis output to Obsidian-compatible markdown with engagement tracking.

### MCP Host

Thane hosts MCP servers as stdio subprocesses, bridging their tools into the
agent loop. Tool filtering via `include_tools` and capability tag assignment.
See [Delegation](delegation.md).

## Technology Choices

| Choice | Rationale |
|--------|-----------|
| **Go** | Single binary, excellent concurrency, no runtime deps |
| **SQLite** | Embedded, reliable, proven at scale |
| **YAML** | Human-readable config with env var substitution |
| **OpenAI-compatible API** | Broad ecosystem compatibility |
| **Markdown talents** | Transparent, editable, version-controlled behavior |
| **goldmark** | Markdown to HTML for email MIME conversion |
| **go-message** | RFC 5322 MIME parsing and composition |
| **go-imap/v2** | Modern IMAP client with extension support |
| **go-vcard** | vCard parsing and serialization for contacts |
| **go-webdav** | CardDAV server (RFC 6352) |
| **go-qrcode** | QR code generation for contact sharing |
| **htmx** | Lightweight frontend interactivity for the dashboard |

## License

Apache 2.0 — aligned with Home Assistant.
