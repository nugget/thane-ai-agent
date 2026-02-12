# Thane

[![Go Reference](https://pkg.go.dev/badge/github.com/nugget/thane-ai-agent.svg)](https://pkg.go.dev/github.com/nugget/thane-ai-agent)

> An autonomous AI agent that learns, remembers, and acts.

**Thane** is an autonomous AI agent that manages your home and life — not just your smart home, but anything you point it at. It smartly selects between local models via Ollama and cloud-based frontier models, optimizing for performance or cost as the task demands. Persistent memory means it learns and grows over time.

If you have Home Assistant, Thane drops in as a native conversation agent and blows the doors off the built-in Assist. If you don't, Thane is still a capable, self-hosted AI with persistent memory and tool use that might just convince you to get one.

## What Makes Thane Different

**Autonomous, not subjected.** Home Assistant's Assist can only see entities you pre-select. Thane has full API access — it discovers devices, understands context, and takes action on its own. Ask *"why is the garage warm?"* and it correlates HVAC, weather, and door states without you wiring anything up.

**Memory that persists.** Thane remembers facts about your home, your preferences, and your routines. It learns over time. Tell it once that you like the lights dim after 10pm and it just... knows.

**Local-first.** Runs entirely on Ollama. Your conversations, your data, your hardware. Cloud models available as fallback, never required.

**Single binary.** Written in Go. No Python environments, no dependency hell. One command: `thane`.

📐 **[ARCHITECTURE.md](ARCHITECTURE.md)** — Full system design, component overview, and roadmap.

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

- **Full Home Assistant integration** — entity discovery, state queries, service calls, WebSocket events
- **Smart device control** — natural language to action with fuzzy entity matching
- **Semantic memory** — learns and recalls facts with embeddings-based search
- **Web search** — SearXNG and Brave Search providers with pluggable architecture
- **Web fetch** — extract readable content from any URL
- **Shell execution** — run commands on the host (configurable safety guardrails)
- **Talent system** — customize agent behavior with markdown files
- **Model routing** — selects the right model for each task (speed vs. quality vs. cost)
- **Checkpoint/restore** — survives restarts without losing context
- **Dual-port architecture** — native API + Ollama-compatible API simultaneously
- **Structured logging** — text or JSON format (`log_format` in config)

## Architecture

```
User ──→ API Server ──→ Agent Loop ──→ Response
                           │
              ┌────────────┼────────────┐
              ↓            ↓            ↓
           Memory     Model Router   HA Client
           (SQLite)    (Ollama)      (REST/WS)
```

Thane's agent loop receives a request, assembles context from memory and home state, plans tool calls, executes them (in parallel where possible), and shapes a response.

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

## Roadmap

**Working today:** Conversation agent, HA integration, device control, semantic memory, checkpoints, shell exec, web search (SearXNG + Brave), web fetch, web chat UI, dual-port APIs, Anthropic + Ollama model routing.

**Next up:** Email (IMAP/SMTP), TTS, cron scheduling, proactive event-driven triggers, voice pipeline integration.

**Longer term:** HA Add-on packaging, Apple ecosystem integration, multi-instance deployment, identity system.

See [GitHub Issues](https://github.com/nugget/thane-ai-agent/issues) for the full backlog.

## Name

**Thane** — A Scottish term for a landholder who managed an estate on behalf of the crown. Your home automation estate deserves a capable steward.

## Contributing

Contributions welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

Apache 2.0 — aligned with Home Assistant.
