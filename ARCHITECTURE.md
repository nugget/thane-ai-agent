# Thane Architecture

> **Thane**: A Scottish landholder who managed an estate on behalf of the crown.
> An autonomous AI agent that learns, remembers, and acts.

## Philosophy

A vibration sensor on a washer tells you it's done. But understanding that you're home, it's been 30 minutes, and you haven't moved the laundry — and gently reminding you before it gets musty — that's the difference between automation and comprehension.

This is what Thane is building toward: a system that notices, understands, and helps. Not through rigid automation rules, but through genuine contextual awareness.

**Understanding over Rules.** Traditional home automation fires events. Thane comprehends situations. The difference between "washer stopped" and "laundry needs attention before it mildews."

**Boring Tech, Creative Application.** Go, SQLite, MQTT, Home Assistant. Mature, stable, documented. The innovation isn't in the stack — it's in how these pieces compose to create something that can actually care about your environment.

**Open Source as Philosophy.** This isn't a product seeking users. It's an idea seeking evolution. Every component is accessible, every decision documented. Someone will fork this and do something we never imagined. That's the point.

### What We're Not Building

- Not another voice assistant (though voice is one interface)
- Not another automation platform (though we integrate with automation)
- Not a business model (this is gift culture)

### What We Are Building

A nervous system for living spaces. Sensors that notice. Anticipations that remember what matters. Context engines that understand. All working together to create an environment that's genuinely helpful without being intrusive.

*Wake frequently, speak rarely. The difference is the craft.*

---

## The Core Insight

Thane is an **autonomous agent** — an LLM with persistent memory, tool use, and the ability to act on your behalf. It's not constrained to a pre-defined set of capabilities; it discovers what's available and reasons about how to help.

Home Assistant is Thane's flagship integration, but not a requirement. With HA, Thane becomes a dramatically more capable replacement for the built-in Assist — querying any entity, discovering devices dynamically, and taking intelligent action without pre-selection. Without HA, Thane is still a capable self-hosted AI with semantic memory, shell access, and file operations.

## Design Principles

### Local-First, Cloud-Optional

Thane can run entirely on Ollama. Every conversation stays on your hardware. Cloud models are available for complex reasoning, but nothing requires them.

### Memory as Foundation

Memory isn't bolted on — it's foundational:

- **Semantic facts** — Learned knowledge with embeddings for natural language recall
- **Conversations** — Context that persists across sessions
- **Checkpoints** — Full state snapshots for crash recovery

Memory is queryable: the agent can ask "what do I know about the garage?" and get structured answers through semantic search.

### Talent-Driven Behavior

Agent personality and behavioral guidance live in markdown files (`talents/`). This keeps behavior transparent, editable, and version-controlled. Natural language is the configuration mechanism — it carries nuance that structured config cannot.

### Dual-Port Architecture

Thane serves two APIs simultaneously:

- **Port 8080** — Native API (OpenAI-compatible `/v1/chat/completions`). For direct integration, development, and the built-in web chat.
- **Port 11434** — Ollama-compatible API. Home Assistant's native Ollama integration connects here without modification. Thane strips HA's injected tools and system prompts, substituting its own smarter toolset.

This means Thane works with HA out of the box — no custom integration needed.

## Architecture Overview

```
┌───────────────────────────────────────────────────────────────────┐
│                          Thane Agent                               │
├───────────────────────────────────────────────────────────────────┤
│                                                                    │
│  ┌──────────────┐  ┌──────────────┐  ┌─────────────────────────┐  │
│  │  Native API  │  │ Ollama-Compat│  │     Event Sources       │  │
│  │  (port 8080) │  │ (port 11434) │  │  HA WebSocket │ MQTT    │  │
│  └──────┬───────┘  └──────┬───────┘  │  (state_changed)│(Frigate)│ │
│         │                 │          └────────┬────────────────┘  │
│         └─────────────────┼───────────────────┘                   │
│                           ▼                                        │
│  ┌─────────────────────────────────────────────────────────────┐  │
│  │                      Agent Loop                              │  │
│  │                                                              │  │
│  │  Context Assembly → Planning → Delegation → Response         │  │
│  │        ↑                          │                          │  │
│  │    Talents &                 ┌────┴─────┐                    │  │
│  │    Memory                    ↓          ↓                    │  │
│  │                         Direct      Delegate                 │  │
│  │                        (primary)   (local model)             │  │
│  └─────────────────────────────────────────────────────────────┘  │
│         │                │                    │                     │
│         ▼                ▼                    ▼                     │
│  ┌─────────────┐  ┌────────────┐  ┌───────────────────────────┐  │
│  │   Memory    │  │   Model    │  │      Integrations         │  │
│  │   Store     │  │   Router   │  │  HA (REST/WS) │ MCP Host  │  │
│  │  (SQLite)   │  │ (profiles) │  │  Shell │ Files │ Search   │  │
│  └─────────────┘  └────────────┘  └───────────────────────────┘  │
│                                                                    │
│  ┌─────────────┐  ┌────────────┐  ┌───────────────────────────┐  │
│  │ Anticipation│  │  Scheduler │  │   MQTT Publisher          │  │
│  │   Engine    │  │  (cron)    │  │   (sensor telemetry)      │  │
│  └─────────────┘  └────────────┘  └───────────────────────────┘  │
│                                                                    │
└───────────────────────────────────────────────────────────────────┘
```

## Components

### Agent Loop

The core reasoning cycle:

1. **Context Assembly** — Gather relevant memory, talents, and the current request
2. **Planning** — Determine what information or actions are needed
3. **Delegation** — Primary model orchestrates; tool-heavy work is delegated to local models
4. **Tool Execution** — Run tool calls (parallel where possible), via native tools or MCP
5. **Response Shaping** — Format output for the requesting interface

### Delegation System

The primary model (cloud or large local) focuses on reasoning and orchestration. When tool-heavy work is needed — HA queries, file operations, shell commands — it delegates to smaller, faster local models that execute the task and return results.

This creates a cost/performance sweet spot: frontier-model intelligence for planning, local-model efficiency for execution. A 20B parameter model running locally can search HA entities, call services, and verify results at zero API cost.

Delegation is guided by **talents** — markdown files that teach the primary model how to write effective delegation prompts, which tools are available to delegates, and what patterns to follow (e.g., search → act → verify for HA device control).

### Tools

**Native Tools:**

| Tool | Description |
|------|-------------|
| `control_device` | Natural language device control with fuzzy entity matching |
| `find_entity` | Smart entity discovery across all HA domains |
| `get_state` | Current state of any entity (returns 404 for nonexistent entities) |
| `list_entities` | Browse entities by domain or pattern |
| `call_service` | Direct HA service invocation |
| `thane_delegate` | Delegate tool-heavy tasks to local models |
| `remember_fact` | Store knowledge with semantic embeddings |
| `recall_fact` | Retrieve knowledge by category or natural language |
| `forget_fact` | Remove stored knowledge |
| `archive_search` | Full-text search across conversation history |
| `session_working_memory` | Read/write scratchpad for active session context |
| `schedule_task` | Time-based future actions |
| `create_anticipation` | Event-based triggers |
| `web_search` | Search the web via SearXNG or Brave Search |
| `web_fetch` | Fetch and extract readable content from a URL |
| `exec` | Host shell command execution (configurable safety) |
| `read_file` / `write_file` / `edit_file` | Workspace file operations |

**MCP Tools (via Model Context Protocol):**

Thane hosts MCP servers as subprocesses, bridging their tools into the agent loop. Example: [ha-mcp](https://github.com/karimkhaleel/ha-mcp) provides 90+ Home Assistant tools — search, state queries, service calls, camera images, automation traces, and more.

MCP tools are filtered via `include_tools` to keep delegate context manageable. Tool gating controls which tools are visible at each stage: the primary model sees orchestration tools (delegate, memory, archive); delegates see all execution tools (native + MCP).

### Memory Store

SQLite-backed with optional vector search:

- **Facts** — Categorized knowledge (user, home, device, routine, preference) with embeddings
- **Conversations** — Full history with tool calls
- **Checkpoints** — Compressed state snapshots triggered by message count, shutdown, or manual request

### Model Router

Intent-based routing profiles select the right model for each task:

| Profile | Intent | Typical Model |
|---------|--------|---------------|
| `latest` | Default conversation | Best local model |
| `premium` | Complex reasoning, orchestration | Frontier cloud model |
| `command` | Direct tool execution | Fast local model |
| `trigger` | Event-driven responses | Fast local model |
| `ops` | Infrastructure tasks (all tools visible) | Frontier cloud model |
| `peer` | Cross-agent communication | Local model |
| `local` | Force local-only | Best local model |

Profile names are vendor-neutral — they describe intent, not model names. The router maps profiles to specific models based on configured quality/speed scores, with automatic failover and checkpoint before model switch.

### Home Assistant Integration (optional)

When configured, deep HA integration via multiple protocols:

- **REST API** — State queries, service calls, template rendering
- **WebSocket API** — Persistent connection for real-time `state_changed` events, area/device/entity registry access. Client-side filtering by entity glob patterns (e.g., `person.*`, `binary_sensor.*door*`). This is the official HA event bus — the same mechanism used by the HA frontend and mobile apps.
- **MCP** — ha-mcp server provides 90+ tools for comprehensive HA interaction, bridged into the agent loop via stdio transport
- **MQTT** — Thane publishes its own sensor telemetry (uptime, token usage, model info) as HA-discoverable MQTT entities. Also subscribes to Frigate events for NVR-driven triggers.

**Protocol separation principle:** Each protocol is used for what it does best. WebSocket for HA state subscriptions (real-time, official API, no config changes to HA). MQTT for Frigate (publishes natively) and Thane's own telemetry. MCP for rich HA tool access. REST for simple queries.

### Talent System

Markdown files in `talents/` that guide agent behavior:

- `conversational.md` — Tone and style
- `time-awareness.md` — Timezone handling, natural time formatting
- `spatial-reasoning.md` — Understanding home layout
- `proactive-curiosity.md` — When to explore vs. wait
- `channel-awareness.md` — Adapting to context (voice vs. text vs. chat)

Talents are injected into the system prompt. Instance-specific talents can layer on top of upstream defaults.

### Anticipation Engine

The anticipation system enables event-driven behavior. Rather than polling, Thane creates **anticipations** — conditions it's watching for — and resolves them when matching events arrive.

- **Anticipations** are stored in SQLite with trigger conditions (entity patterns, state transitions)
- **Event sources** feed the matcher: HA WebSocket `state_changed` events, MQTT messages (Frigate), scheduled wakes
- **Resolution** triggers an agent loop run with context about what happened and why Thane was watching for it

Example: Thane notices you left home (person tracker → `not_home`). It creates an anticipation for your return. When the person entity changes back to `home`, the anticipation fires, and Thane can welcome you back, report what happened while you were away, or adjust the house.

### Scheduler

Time-based task execution with cron-style scheduling. Tasks can fire into the agent loop as wake events, enabling periodic behavior (heartbeat checks, memory consolidation, proactive notifications).

### MQTT Publisher

Thane publishes its own operational telemetry as HA-discoverable MQTT entities:

- `sensor.aimee_thane_uptime` — service uptime
- `sensor.aimee_thane_tokens_today` — daily token consumption
- `sensor.aimee_thane_default_model` — current routing model
- `sensor.aimee_thane_last_request` — timestamp of last interaction
- `sensor.aimee_thane_version` — running version

These appear automatically in HA via MQTT discovery, enabling dashboards and automations based on Thane's own state.

## Technology Choices

| Choice | Rationale |
|--------|-----------|
| **Go** | Single binary, excellent concurrency, no runtime deps |
| **SQLite** | Embedded, reliable, proven at scale |
| **YAML** | Human-readable config with env var substitution |
| **OpenAI-compatible API** | Broad ecosystem compatibility |
| **Markdown talents** | Transparent, editable, version-controlled behavior |

## Deployment

### Standalone Binary

```bash
just build
./dist/thane-*/thane -config config.yaml serve
```

### Service Installation

**macOS** — User launch agent (no sudo):
```bash
just install && just service-install
```

**Linux** — systemd with dedicated user and security hardening:
```bash
sudo just install && sudo just service-install
```

See [README.md](README.md) for detailed deployment instructions.

## Roadmap

### Phase 1: Foundation ✅
- OpenAI-compatible API, HA REST client, agent loop, conversation memory, Ollama-compatible dual-port

### Phase 2: Intelligence ✅
- WebSocket client, model routing, checkpoint/restore, semantic memory, control_device, shell exec, web search (SearXNG + Brave), web fetch, Anthropic provider, httpkit networking layer

### Phase 3: Autonomy 🚧
- ✅ MCP host support (ha-mcp, extensible)
- ✅ Delegation system (primary orchestrates, local models execute)
- ✅ Intent-based routing profiles
- ✅ MQTT publishing (Thane telemetry as HA entities)
- ✅ Anticipation engine (event-driven triggers)
- ✅ Task scheduler
- ✅ Session archive with full-text search
- ✅ Session working memory
- 🚧 HA WebSocket state subscriptions (#176)
- 🚧 Scheduler → agent loop wiring (#173)
- ⬜ Email (IMAP/SMTP), TTS, voice pipeline integration

### Phase 4: Ecosystem
- HA Add-on packaging, Apple ecosystem integration, multi-instance deployment, identity system

## License

Apache 2.0 — aligned with Home Assistant.
