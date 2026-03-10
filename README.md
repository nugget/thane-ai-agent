# Thane

[![Go Reference](https://pkg.go.dev/badge/github.com/nugget/thane-ai-agent.svg)](https://pkg.go.dev/github.com/nugget/thane-ai-agent)

> An autonomous AI agent that learns, remembers, and acts.

**Thane** is an autonomous AI agent that manages your home and life — not just your smart home, but anything you point it at. It smartly selects between local models via Ollama and cloud-based frontier models, optimizing for performance or cost as the task demands. Persistent memory means it learns and grows over time.

If you have Home Assistant, Thane drops in as a native conversation agent and blows the doors off the built-in Assist. If you don't, Thane is still a capable, self-hosted AI with persistent memory and tool use that might just convince you to get one.

## What Makes Thane Different

**Autonomous, not subjected.** Home Assistant's Assist can only see entities you pre-select. Thane has full API access — it discovers devices, understands context, and takes action on its own. Ask *"why is the garage warm?"* and it correlates HVAC, weather, and door states without you wiring anything up.

**Memory that persists.** Thane remembers facts about your home, your preferences, and your routines. It learns over time. Tell it once that you like the lights dim after 10pm and it just... knows.

**Local-first.** Can run entirely on Ollama. Your conversations, your data, your hardware. Cloud models available as fallback, never required.

**Single binary.** Written in Go. No Python environments, no dependency hell. One command: `thane`.

📖 **[Your First Thane](docs/guide.md)** — Complete guide for HA users: hardware, models, deployment, and building a relationship with your agent.

📐 **[ARCHITECTURE.md](ARCHITECTURE.md)** — Full system design and component overview.

## Prerequisites

- [Go](https://go.dev/) 1.24+ (to build from source)
- [just](https://just.systems/) (command runner — replaces Makefiles)
- [Ollama](https://ollama.ai/) (for local model inference)
- Home Assistant (optional, for smart home integration)

## Quick Start

```bash
# Clone and build
git clone https://github.com/nugget/thane-ai-agent.git
cd thane-ai-agent
just build

# Initialize ~/Thane with config, talents, and persona
just init
# Edit ~/Thane/config.yaml with your Ollama URL and (optionally) Home Assistant token

# Run
just version          # verify the build
just serve            # start the server
```

Thane serves two APIs:
- **Port 8080** — Native API (OpenAI-compatible chat/completions)
- **Port 11434** — Ollama-compatible API (for Home Assistant's native integration)

To connect Home Assistant: point an Ollama integration at `http://thane-host:11434`, select model `thane:latest`.

See [docs/getting-started.md](docs/getting-started.md) for detailed setup and deployment, [docs/homeassistant.md](docs/homeassistant.md) for HA integration.

## Features

- **Full Home Assistant integration** — entity discovery, state queries, service calls, real-time WebSocket event subscriptions
- **Smart device control** — natural language to action with fuzzy entity matching
- **Native email** — IMAP read/search + SMTP send/reply with markdown-to-MIME conversion, trust zone gating, and auto-Bcc audit trail
- **Email polling** — scheduled IMAP checks with high-water mark tracking, wakes agent only when new messages arrive
- **Signal messaging bridge** — inbound/outbound messaging via signal-cli with trust zone gating and rate limiting
- **Contact directory** — vCard-aligned contact storage with trust zones (admin/household/trusted/known) for access control across email, messaging, notifications, and compute routing
- **CardDAV server** — native contact sync with macOS Contacts.app, iOS, and Thunderbird via RFC 6352
- **VCF import/export** — bulk contact exchange with QR code generation for mobile sharing
- **Actionable notifications** — provider-agnostic push notifications with human-in-the-loop callbacks, timeout escalation, and decision tracking
- **MCP tool integration** — extends capabilities via Model Context Protocol servers (e.g., ha-mcp for 90+ HA tools)
- **Capability tag system** — dynamic tool loading based on semantic tags; agents start lightweight and activate tools on demand
- **Delegation system** — orchestrator model plans, local models execute tool-heavy tasks at zero API cost, with execution summaries
- **Model routing** — quality/speed/cost scoring with explicit hints (quality_floor, prefer_speed, local_only) across all code paths
- **Semantic memory** — learns and recalls facts with embeddings-based search
- **Self-reflection** — periodic ego.md analysis for personality development and behavioral pattern consolidation
- **Session management** — close, checkpoint, and split sessions with carry-forward context
- **Anticipation engine** — event-driven triggers with per-anticipation model routing
- **Task scheduler** — cron-style scheduling with model/routing overrides
- **RSS/Atom feed monitoring** — follow feeds and YouTube channels with periodic polling and new-entry detection
- **Media analysis vault** — structured content analysis output to Obsidian-compatible markdown with engagement tracking
- **Web dashboard** — operational visibility with data browsers for contacts, facts, sessions, tasks, and anticipations
- **GitHub/Forgejo integration** — issues, PRs, code review, and search via 18 native forge tools
- **Web search** — SearXNG and Brave Search providers with pluggable architecture
- **Web fetch** — extract readable content from any URL
- **Shell execution** — run commands on the host (configurable safety guardrails)
- **Talent system** — customize agent behavior with tag-filtered markdown files
- **Checkpoint/restore** — survives restarts without losing context
- **Dual-port architecture** — native API + Ollama-compatible API simultaneously
- **Unified database** — conversations, memory, scheduler, and anticipations in a single `thane.db`
- **Operational state store** — generic KV persistence with TTL support for poller cursors, feature flags, session preferences
- **Event bus** — in-process publish/subscribe for execution tracing and operational observability
- **MQTT telemetry** — publishes agent state as HA-discoverable entities
- **Structured logging** — text or JSON format (`log_format` in config)

## Architecture

```
                    ┌─────────────────────────────┐
                    │        Event Sources         │
                    │  HA WS │ MQTT │ Email        │
                    │  Signal │ RSS/Atom Feeds     │
                    │  Scheduler │ Anticipations   │
                    └──────────┬──────────────────┘
                               ▼
User ──→ API Server ──→ Agent Loop ──→ Response
              │            │
              │   ┌────────┼──────────────┐
              │   ↓        ↓              ↓
              │ Memory  Model Router  Integrations
              │ (SQLite) (scoring)   (HA, Email, Signal,
              │            │          MCP, Forge, shell)
              │            ↓
              │        Delegates      Notifications
              │     (local models)   (HITL callbacks)
              │
              ├──→ Web Dashboard
              └──→ CardDAV Server
```

Thane's agent loop receives a request — from a user, event trigger, or scheduled task — assembles context from memory, contacts, and home state, activates capability tags for the task, plans tool calls, delegates tool-heavy work to local models, and shapes a response. The orchestrator model plans; delegates execute.

## CLI

```
$ thane --help
Thane - Autonomous Home Assistant Agent

Usage: thane [flags] <command> [args]

Commands:
  serve    Start the API server
  ask      Ask a single question (for testing)
  ingest   Import markdown docs into fact store
  version  Show version information

Flags:
  -config <path>    Path to config file (default: auto-discover)
  -o, --output fmt  Output format: text (default) or json
```

Config is auto-discovered from: `./config.yaml`, `~/Thane/config.yaml`, `~/.config/thane/config.yaml`, `/config/config.yaml`, `/usr/local/etc/thane/config.yaml`, `/etc/thane/config.yaml`

## Documentation

| Document | Description |
|----------|-------------|
| [ARCHITECTURE.md](ARCHITECTURE.md) | System design, components, and tools reference |
| [docs/guide.md](docs/guide.md) | Complete getting-started guide for HA users |
| [docs/getting-started.md](docs/getting-started.md) | Setup and deployment |
| [docs/homeassistant.md](docs/homeassistant.md) | Home Assistant integration |
| [docs/delegation.md](docs/delegation.md) | Delegation system deep dive |
| [docs/memory.md](docs/memory.md) | Memory architecture |
| [docs/routing-profiles.md](docs/routing-profiles.md) | Model routing and profiles |
| [docs/context-layers.md](docs/context-layers.md) | Context assembly layers |
| [docs/trust-architecture.md](docs/trust-architecture.md) | Trust zone design and structural enforcement |

## Roadmap

**Working today:** Conversation agent, HA integration (REST + WebSocket + MCP), delegation with execution summaries, capability tag system, native email (IMAP/SMTP), email polling, Signal messaging bridge, vCard-aligned contact directory with trust zones (admin/household/trusted/known), CardDAV server for native contact sync, VCF import/export with QR codes, actionable notifications with HITL callbacks, model routing with quality/speed hints, semantic memory, self-reflection, session management, anticipation engine, task scheduler, RSS/Atom feed monitoring, media analysis vault, web dashboard with data browsers, GitHub/Forgejo integration (18 tools), event bus, MQTT telemetry, web search/fetch, shell exec, operational state store with TTL support, unified database (thane.db), execution tracing.

**Next up:** Egress gate for outbound message control (#336), dynamic model registry (#93), email trust-zone triage on poll, notification channel expansion (Signal delivery).

**Longer term:** HA Add-on packaging, Apple ecosystem integration, git-backed identity store (#43), voice pipeline integration, IMAP IDLE for push notifications, multi-instance deployment.

See [GitHub Issues](https://github.com/nugget/thane-ai-agent/issues) for the full backlog.

## Name

**Thane** — A Scottish term for a landholder who managed an estate on behalf of the crown. Your home automation estate deserves a capable steward.

## Contributing

Contributions welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

Apache 2.0 — aligned with Home Assistant.
