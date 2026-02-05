# Thane Architecture

> **Thane**: A Scottish landholder who managed an estate on behalf of the crown.
> Your Home Assistant already has an agent. We make it *autonomous*.

## The Problem

Home Assistant's built-in Assist uses a **subjected agent** model: an LLM constrained
to a pre-selected set of entities exposed via the conversation API. This works for
simple commands ("turn off the lights") but breaks down for real autonomy:

- The agent can only see what you explicitly expose
- No ability to discover or query entities dynamically
- No persistent memory across conversations
- No proactive actions or monitoring
- Limited to request-response patterns

Thane is an **autonomous agent** that breaks these constraints.

## Core Insight

Home Assistant already exposes a comprehensive REST/WebSocket API. An autonomous
agent doesn't need entity pre-selection — it can query the API directly to understand
state, discover devices, and take action. The LLM becomes a **reasoning layer** over
HA's full capabilities, not a constrained responder to a curated subset.

## Design Principles

### 1. Ollama-Compatible API

Thane exposes an OpenAI-compatible chat/completions endpoint that HA (and other
tools) can use as a drop-in LLM backend. The magic happens inside: requests that
would normally just prompt an LLM instead route through an agent loop that can:

- Query HA state before responding
- Call HA services as tool invocations
- Maintain context across turns
- Act proactively on triggers

### 2. Parallel Over Serial

Unlike event loops that serialize all execution per-session, Thane supports:

- **Parallel tool execution**: Fetch multiple API endpoints simultaneously
- **Background tasks**: Long-running operations don't block conversation
- **Concurrent sessions**: Multiple conversations without mutual blocking

State consistency is maintained at the *data layer*, not by blocking execution.

### 3. Memory as First-Class Citizen

Memory isn't bolted on — it's foundational:

- **Entity memory**: Learned facts about devices, rooms, people
- **Preference memory**: User patterns, schedules, automations
- **Conversation memory**: Context that persists across sessions
- **Relationship memory**: Connections between entities (this light is in that room)

Memory is queryable, not just appended. The agent can ask "what do I know about
the garage?" and get structured answers.

### 4. Smart Model Routing

Not every task needs the same model:

| Task Type | Model Choice |
|-----------|-------------|
| Quick status check | Local small model (fast, free) |
| Complex reasoning | Cloud large model (capable, costly) |
| Entity extraction | Specialized fine-tuned model |
| Embeddings | Dedicated embedding model |

Routing is automatic based on task classification, with cost awareness built in.

### 5. Event-Driven, Not Poll-Driven

Thane subscribes to HA's WebSocket event stream:

- State changes trigger evaluation ("motion detected — is anyone home?")
- Schedules are first-class ("every morning at sunrise...")
- Conditions are monitored ("when temperature drops below 65...")

The agent can be proactive because it *sees* what's happening, not just responds to prompts.

### 6. Simple Configuration

No 6-level nested config objects. A Thane config looks like:

```yaml
homeassistant:
  url: http://homeassistant.local:8123
  token: ${HA_TOKEN}

models:
  default: ollama/llama3:8b
  reasoning: anthropic/claude-sonnet-4
  
memory:
  path: ./memory
  
listen:
  port: 8080
```

Channel-specific quirks are handled by adapters, not config sprawl.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                         Thane Agent                              │
├─────────────────────────────────────────────────────────────────┤
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐  │
│  │   OpenAI    │  │    Event    │  │       Scheduler         │  │
│  │  Compat API │  │  Listener   │  │   (cron, conditions)    │  │
│  └──────┬──────┘  └──────┬──────┘  └───────────┬─────────────┘  │
│         │                │                     │                 │
│         └────────────────┼─────────────────────┘                 │
│                          ▼                                       │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │                     Agent Loop                             │  │
│  │  ┌─────────┐  ┌──────────┐  ┌──────────┐  ┌────────────┐  │  │
│  │  │ Context │  │ Planning │  │   Tool   │  │  Response  │  │  │
│  │  │ Assembly│──│  Engine  │──│ Executor │──│  Shaping   │  │  │
│  │  └─────────┘  └──────────┘  └──────────┘  └────────────┘  │  │
│  └───────────────────────────────────────────────────────────┘  │
│         │                │                     │                 │
│         ▼                ▼                     ▼                 │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐  │
│  │   Memory    │  │   Model     │  │    Home Assistant       │  │
│  │   Store     │  │   Router    │  │    Client               │  │
│  └─────────────┘  └─────────────┘  └─────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
        ┌──────────┐   ┌──────────┐   ┌──────────────┐
        │  Ollama  │   │  Claude  │   │ Home Assistant│
        │  (local) │   │  (cloud) │   │    REST/WS   │
        └──────────┘   └──────────┘   └──────────────┘
```

## Components

### API Server

Exposes OpenAI-compatible `/v1/chat/completions` endpoint. Requests include:
- Standard chat messages
- Optional conversation ID for session continuity
- Tool definitions (auto-populated with HA capabilities)

### Event Listener

Connects to HA WebSocket API and:
- Maintains subscription to state changes
- Filters events by relevance (configurable)
- Routes significant events to agent evaluation

### Scheduler

Manages time-based and condition-based triggers:
- Cron-style schedules
- Sunrise/sunset relative times
- Condition monitoring (temperature, presence, etc.)

### Agent Loop

The core reasoning cycle:

1. **Context Assembly**: Gather relevant memory, recent events, current request
2. **Planning Engine**: Determine what information/actions are needed
3. **Tool Executor**: Run tools in parallel where possible
4. **Response Shaping**: Format output for the requesting interface

### Memory Store

Structured storage for:
- Conversations (with compaction)
- Entity facts (learned and explicit)
- User preferences
- Automation history

SQLite-backed with optional vector search for semantic queries.

### Model Router

Selects appropriate model based on:
- Task complexity (heuristics + classifier)
- Cost budget
- Latency requirements
- Capability requirements (vision, function calling, etc.)

### Home Assistant Client

Full HA API client supporting:
- REST API for state queries and service calls
- WebSocket for real-time events
- Template rendering
- Area/device/entity registry access

## Key Differences from OpenClaw

| Aspect | OpenClaw | Thane |
|--------|----------|-------|
| Execution | Serial per-session | Parallel with state isolation |
| Memory | Files + bolted-on search | First-class structured store |
| Model selection | Failover chain | Task-based smart routing |
| Configuration | Deep nested objects | Flat with sensible defaults |
| Target | General assistant | Home automation focused |
| Tool execution | One at a time | Parallel with dependency graph |
| Events | Request-response + heartbeat | Native event subscription |

## Roadmap

### Phase 1: Foundation
- [ ] OpenAI-compatible API server
- [ ] Basic HA client (REST)
- [ ] Simple agent loop (single model)
- [ ] Conversation memory

### Phase 2: Intelligence
- [ ] HA WebSocket event stream
- [ ] Model routing
- [ ] Parallel tool execution
- [ ] Structured memory store

### Phase 3: Autonomy
- [ ] Scheduler with conditions
- [ ] Proactive actions
- [ ] Learning from corrections
- [ ] Multi-agent coordination

### Phase 4: Polish
- [ ] HA integration (custom component)
- [ ] Web dashboard
- [ ] Voice pipeline integration
- [ ] Community tool library

## Technology Choices

- **Language**: Go (performance, single binary, good concurrency primitives)
- **Database**: SQLite (embedded, reliable, familiar)
- **Vector search**: sqlite-vec or built-in cosine similarity
- **Config**: YAML with env var substitution
- **API**: OpenAI-compatible (broad tooling support)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

Apache 2.0 — aligned with Home Assistant's license.
