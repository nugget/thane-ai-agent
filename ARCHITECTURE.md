# Thane Architecture

> **Thane**: A Scottish landholder who managed an estate on behalf of the crown.
> Your home already has an agent. We make it *autonomous*.

## The Core Insight

Home Assistant exposes a comprehensive REST and WebSocket API. An autonomous agent doesn't need entity pre-selection — it can query the API directly to understand state, discover devices, and take action. The LLM becomes a **reasoning layer** over HA's full capabilities, not a constrained responder to a curated subset.

This is the key difference between a **subjected agent** (Assist) and an **autonomous agent** (Thane).

## Design Principles

### Local-First, Cloud-Optional

Thane runs on Ollama. Every conversation stays on your hardware. Cloud models are available as fallback for complex reasoning, but nothing requires them.

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
┌──────────────────────────────────────────────────────────────┐
│                        Thane Agent                            │
├──────────────────────────────────────────────────────────────┤
│                                                               │
│  ┌──────────────┐  ┌──────────────┐  ┌────────────────────┐  │
│  │  Native API  │  │ Ollama-Compat│  │   Event Listener   │  │
│  │  (port 8080) │  │ (port 11434) │  │   (HA WebSocket)   │  │
│  └──────┬───────┘  └──────┬───────┘  └─────────┬──────────┘  │
│         └─────────────────┼────────────────────┘              │
│                           ▼                                   │
│  ┌────────────────────────────────────────────────────────┐   │
│  │                     Agent Loop                          │   │
│  │                                                         │   │
│  │  Context Assembly → Planning → Tool Execution →         │   │
│  │                                          Response       │   │
│  └────────────────────────────────────────────────────────┘   │
│         │                │                    │                │
│         ▼                ▼                    ▼                │
│  ┌─────────────┐  ┌────────────┐  ┌──────────────────────┐   │
│  │   Memory    │  │   Model    │  │   Home Assistant     │   │
│  │   Store     │  │   Router   │  │   Client (REST/WS)   │   │
│  │  (SQLite)   │  │  (Ollama)  │  │                      │   │
│  └─────────────┘  └────────────┘  └──────────────────────┘   │
│                                                               │
└──────────────────────────────────────────────────────────────┘
```

## Components

### Agent Loop

The core reasoning cycle:

1. **Context Assembly** — Gather relevant memory, talents, and the current request
2. **Planning** — Determine what information or actions are needed
3. **Tool Execution** — Run tool calls (parallel where possible)
4. **Response Shaping** — Format output for the requesting interface

### Tools

| Tool | Description |
|------|-------------|
| `control_device` | Natural language device control with fuzzy entity matching |
| `find_entity` | Smart entity discovery across all HA domains |
| `get_state` | Current state of any entity |
| `list_entities` | Browse entities by domain or pattern |
| `call_service` | Direct HA service invocation |
| `remember_fact` | Store knowledge with semantic embeddings |
| `recall_fact` | Retrieve knowledge by category or natural language |
| `forget_fact` | Remove stored knowledge |
| `schedule_task` | Time-based future actions |
| `create_anticipation` | Event-based triggers |
| `exec` | Host shell command execution (configurable safety) |
| `read_file` / `write_file` / `edit_file` | Workspace file operations |

### Memory Store

SQLite-backed with optional vector search:

- **Facts** — Categorized knowledge (user, home, device, routine, preference) with embeddings
- **Conversations** — Full history with tool calls
- **Checkpoints** — Compressed state snapshots triggered by message count, shutdown, or manual request

### Model Router

Selects models based on task complexity, speed requirements, and cost. Maintains an audit trail of routing decisions. Supports automatic failover with checkpoint before model switch.

### Home Assistant Client

Full HA API integration:

- **REST** — State queries, service calls, template rendering
- **WebSocket** — Persistent connection for real-time events, area/device/entity registry access

### Talent System

Markdown files in `talents/` that guide agent behavior:

- `conversational.md` — Tone and style
- `time-awareness.md` — Timezone handling, natural time formatting
- `spatial-reasoning.md` — Understanding home layout
- `proactive-curiosity.md` — When to explore vs. wait
- `channel-awareness.md` — Adapting to context (voice vs. text vs. chat)

Talents are injected into the system prompt. Instance-specific talents can layer on top of upstream defaults.

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
go build -o thane ./cmd/thane
./thane -config config.yaml serve
```

### Docker

```bash
docker run -d -p 8080:8080 -p 11434:11434 \
  -v thane-data:/data \
  -e HA_URL=http://homeassistant.local:8123 \
  -e HA_TOKEN=your_token \
  ghcr.io/nugget/thane:latest
```

### Home Assistant Add-on (planned)

Native HA Add-on packaging is on the roadmap.

## Roadmap

### Phase 1: Foundation ✅
- OpenAI-compatible API, HA REST client, agent loop, conversation memory, Ollama-compatible dual-port

### Phase 2: Intelligence ✅
- WebSocket client, model routing, checkpoint/restore, semantic memory, control_device, shell exec

### Phase 3: Autonomy 🚧
- Wire events to anticipation triggers, proactive notifications, intent-parser architecture

### Phase 4: Ecosystem
- HA Add-on packaging, Apple ecosystem integration, companion app, multi-instance deployment, git-backed identity store

## License

Apache 2.0 — aligned with Home Assistant.
