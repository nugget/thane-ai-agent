# Thane

> Your home already has an agent. We make it *autonomous*.

**Thane** is an autonomous AI agent that manages your home — and anything else you point it at. It runs on local models via Ollama, keeps a persistent memory, and talks to your smart home through natural conversation.

If you have Home Assistant, Thane drops in as a native conversation agent and blows the doors off the built-in Assist. If you don't, Thane is still a capable, self-hosted AI with persistent memory and tool use that might just convince you to get one.

## What Makes Thane Different

**Autonomous, not subjected.** Home Assistant's Assist can only see entities you pre-select. Thane has full API access — it discovers devices, understands context, and takes action on its own. Ask *"why is the garage warm?"* and it correlates HVAC, weather, and door states without you wiring anything up.

**Memory that persists.** Thane remembers facts about your home, your preferences, and your routines. It learns over time. Tell it once that you like the lights dim after 10pm and it just... knows.

**Local-first.** Runs entirely on Ollama. Your conversations, your data, your hardware. Cloud models available as fallback, never required.

**Single binary.** Written in Go. No Python environments, no dependency hell. Build it, run it, done.

## Quick Start

```bash
# Build
go build -o thane ./cmd/thane

# Configure (see config.example.yaml for all options)
cp config.example.yaml config.yaml
# Edit config.yaml with your Ollama URL and (optionally) Home Assistant token

# Run
./thane -config config.yaml serve

# Test
./thane -config config.yaml ask "Hello, who are you?"
```

Thane serves two APIs:
- **Port 8080** — Native API (OpenAI-compatible chat/completions)
- **Port 11434** — Ollama-compatible API (for Home Assistant's native integration)

To connect Home Assistant: point an Ollama integration at `http://thane-host:11434`, select model `thane:latest`.

See [docs/getting-started.md](docs/getting-started.md) for detailed setup, [docs/homeassistant.md](docs/homeassistant.md) for HA integration.

## Features

- **Full Home Assistant integration** — entity discovery, state queries, service calls, WebSocket events
- **Smart device control** — natural language to action with fuzzy entity matching
- **Semantic memory** — learns and recalls facts with embeddings-based search
- **Shell execution** — run commands on the host (configurable safety guardrails)
- **Talent system** — customize agent behavior with markdown files
- **Model routing** — selects the right model for each task (speed vs. quality vs. cost)
- **Checkpoint/restore** — survives restarts without losing context
- **Dual-port architecture** — native API + Ollama-compatible API simultaneously

## Architecture

```
User ──→ API Server ──→ Agent Loop ──→ Response
                           │
              ┌────────────┼────────────┐
              ↓            ↓            ↓
           Memory     Model Router   HA Client
           (SQLite)    (Ollama)      (REST/WS)
```

Thane's agent loop receives a request, assembles context from memory and home state, plans tool calls, executes them (in parallel where possible), and shapes a response. See [ARCHITECTURE.md](ARCHITECTURE.md) for the full design.

## Roadmap

**Working today:** Conversation agent, HA integration, device control, semantic memory, checkpoints, shell exec, web chat UI, dual-port APIs.

**Next up:** Proactive event-driven triggers, companion app notifications, voice pipeline integration, Apple ecosystem access.

**Longer term:** HA Add-on packaging, multi-instance deployment, identity system with cryptographic integrity.

See [GitHub Issues](https://github.com/nugget/thane-ai-agent/issues) for the full backlog.

## Name

**Thane** — A Scottish term for a landholder who managed an estate on behalf of the crown. Your home automation estate deserves a capable steward.

## Contributing

Contributions welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

Apache 2.0 — aligned with Home Assistant.
